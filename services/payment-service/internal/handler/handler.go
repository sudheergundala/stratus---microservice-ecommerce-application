// Package handler implements the HTTP API: payment endpoints and health probes.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/stratus/payment-service/internal/model"
	"github.com/stratus/payment-service/internal/repo"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// Handler serves the payment API.
type Handler struct {
	log      *slog.Logger
	repo     repo.Repository
	provider *http.Client // has a timeout, so a hung provider cannot hang us
	draining atomic.Bool
}

// New returns a Handler.
func New(log *slog.Logger, r repo.Repository, provider *http.Client) *Handler {
	return &Handler{log: log, repo: r, provider: provider}
}

// Routes returns the HTTP routes.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /payments", h.createPayment)
	mux.HandleFunc("GET /payments/{id}", h.getPayment)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	return mux
}

// StartDraining makes /readyz fail so load balancers stop sending traffic.
func (h *Handler) StartDraining() { h.draining.Store(true) }

func (h *Handler) createPayment(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 255 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
		return
	}

	var req model.PaymentRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body must be a valid JSON payment")
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	p, created, err := h.repo.CreateOnce(r.Context(), key, func() (model.Payment, error) {
		if err := h.chargeCard(r.Context(), req); err != nil {
			return model.Payment{}, err
		}
		return model.NewPayment(req), nil
	})
	if err != nil {
		h.log.Error("charge failed", "order_id", req.OrderID, "error", err)
		writeError(w, http.StatusBadGateway, "payment provider error; retry with the same Idempotency-Key")
		return
	}
	if !created {
		if !req.Matches(p) {
			writeError(w, http.StatusUnprocessableEntity, "Idempotency-Key already used with different parameters")
			return
		}
		h.log.Info("idempotent replay", "payment_id", p.ID, "order_id", p.OrderID)
		writeJSON(w, http.StatusOK, p)
		return
	}

	// Never log the card number or email.
	h.log.Info("payment captured", "payment_id", p.ID, "order_id", p.OrderID,
		"amount_cents", p.AmountCents, "currency", p.Currency, "card_last4", p.CardLast4)
	h.sendReceiptAsync(p)
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handler) getPayment(w http.ResponseWriter, r *http.Request) {
	p, ok := h.repo.Get(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "payment not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// chargeCard calls the payment provider. Simulated for now; the real call will
// use h.provider, whose timeout bounds how long we wait.
func (h *Handler) chargeCard(ctx context.Context, _ model.PaymentRequest) error {
	_ = h.provider
	select {
	case <-time.After(50 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendReceiptAsync runs in the background. A panic here must never crash the
// process, so it is recovered and logged.
func (h *Handler) sendReceiptAsync(p model.Payment) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				h.log.Error("receipt goroutine panicked", "payment_id", p.ID, "panic", fmt.Sprint(rec))
			}
		}()
		// TODO: publish an event for notification-service instead.
		h.log.Info("receipt queued", "payment_id", p.ID)
	}()
}

// healthz is liveness: the process can respond. It never checks dependencies.
func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is readiness: this instance should receive traffic now.
func (h *Handler) readyz(w http.ResponseWriter, _ *http.Request) {
	if h.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Package handler implements the HTTP API: stock, reservations and health probes.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/stratus/inventory-service/internal/model"
	"github.com/stratus/inventory-service/internal/repo"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// Handler serves the inventory API.
type Handler struct {
	log       *slog.Logger
	store     repo.Store
	dbTimeout time.Duration // upper bound for each request's database work
	draining  atomic.Bool
}

// New returns a Handler.
func New(log *slog.Logger, store repo.Store, dbTimeout time.Duration) *Handler {
	return &Handler{log: log, store: store, dbTimeout: dbTimeout}
}

// Routes returns the HTTP routes.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /stock/{sku}", h.setStock)
	mux.HandleFunc("GET /stock/{sku}", h.getStock)
	mux.HandleFunc("POST /reservations", h.reserve)
	mux.HandleFunc("POST /reservations/{orderID}/commit", h.commit)
	mux.HandleFunc("DELETE /reservations/{orderID}", h.release)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	return mux
}

// StartDraining makes /readyz fail so load balancers stop sending traffic.
func (h *Handler) StartDraining() { h.draining.Store(true) }

func (h *Handler) setStock(w http.ResponseWriter, r *http.Request) {
	sku := r.PathValue("sku")
	if !model.ValidSKU(sku) {
		writeError(w, http.StatusBadRequest, "invalid sku")
		return
	}
	var body struct {
		Available *int64 `json:"available"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Available == nil || *body.Available < 0 {
		writeError(w, http.StatusUnprocessableEntity, "available must be a non-negative integer")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.dbTimeout)
	defer cancel()
	if err := h.store.SetStock(ctx, sku, *body.Available); err != nil {
		h.storeFailed(w, "set stock", err)
		return
	}
	stock, err := h.store.GetStock(ctx, sku)
	if err != nil {
		h.storeFailed(w, "read stock", err)
		return
	}
	h.log.Info("stock set", "sku", sku, "available", stock.Available)
	writeJSON(w, http.StatusOK, stock)
}

func (h *Handler) getStock(w http.ResponseWriter, r *http.Request) {
	sku := r.PathValue("sku")
	if !model.ValidSKU(sku) {
		writeError(w, http.StatusBadRequest, "invalid sku")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.dbTimeout)
	defer cancel()
	stock, err := h.store.GetStock(ctx, sku)
	switch {
	case errors.Is(err, repo.ErrNotFound):
		writeError(w, http.StatusNotFound, "sku not found")
	case err != nil:
		h.storeFailed(w, "read stock", err)
	default:
		writeJSON(w, http.StatusOK, stock)
	}
}

func (h *Handler) reserve(w http.ResponseWriter, r *http.Request) {
	var req model.ReservationRequest
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.dbTimeout)
	defer cancel()
	created, err := h.store.Reserve(ctx, req)
	switch {
	case errors.Is(err, repo.ErrOutOfStock):
		h.log.Info("reservation rejected: insufficient stock", "order_id", req.OrderID)
		writeError(w, http.StatusConflict, "insufficient stock")
	case errors.Is(err, repo.ErrConflict):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		h.storeFailed(w, "reserve", err)
	case created:
		h.log.Info("stock reserved", "order_id", req.OrderID, "items", len(req.Items))
		writeJSON(w, http.StatusCreated, map[string]string{"order_id": req.OrderID, "status": "reserved"})
	default:
		h.log.Info("idempotent replay", "order_id", req.OrderID)
		writeJSON(w, http.StatusOK, map[string]string{"order_id": req.OrderID, "status": "reserved"})
	}
}

func (h *Handler) commit(w http.ResponseWriter, r *http.Request) {
	h.finish(w, r, "commit", h.store.Commit)
}

func (h *Handler) release(w http.ResponseWriter, r *http.Request) {
	h.finish(w, r, "release", h.store.Release)
}

func (h *Handler) finish(w http.ResponseWriter, r *http.Request, action string, fn func(context.Context, string) error) {
	orderID := r.PathValue("orderID")
	ctx, cancel := context.WithTimeout(r.Context(), h.dbTimeout)
	defer cancel()
	err := fn(ctx, orderID)
	switch {
	case errors.Is(err, repo.ErrNotFound):
		writeError(w, http.StatusNotFound, "reservation not found")
	case err != nil:
		h.storeFailed(w, action, err)
	default:
		h.log.Info("reservation "+action, "order_id", orderID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// healthz is liveness: the process can respond. It never checks DynamoDB.
func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is readiness. It deliberately does not call DynamoDB: probes run
// every few seconds on every pod, and AWS control-plane calls are rate limited
// and cost money. A DynamoDB problem shows up as 503s on real requests.
func (h *Handler) readyz(w http.ResponseWriter, _ *http.Request) {
	if h.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) storeFailed(w http.ResponseWriter, action string, err error) {
	h.log.Error("dynamodb call failed", "action", action, "error", err)
	writeError(w, http.StatusServiceUnavailable, "inventory store unavailable; retry")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "request body must be valid JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

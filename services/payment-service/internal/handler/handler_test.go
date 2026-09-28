package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stratus/payment-service/internal/model"
	"github.com/stratus/payment-service/internal/repo"
)

const validBody = `{"order_id":"ord_1","amount_cents":4999,"currency":"USD","card_number":"4111111111111111","email":"sam@example.com"}`

func newTestRoutes() http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(log, repo.NewMemory(), &http.Client{Timeout: time.Second}).Routes()
}

func post(h http.Handler, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(rec *httptest.ResponseRecorder) model.Payment {
	var p model.Payment
	_ = json.NewDecoder(rec.Body).Decode(&p)
	return p
}

func TestCreatePayment(t *testing.T) {
	rec := post(newTestRoutes(), "k1", validBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body)
	}
	if p := decode(rec); p.CardLast4 != "1111" || p.AmountCents != 4999 || !strings.HasPrefix(p.ID, "pay_") {
		t.Fatalf("unexpected payment %+v", p)
	}
}

func TestRetryDoesNotChargeTwice(t *testing.T) {
	h := newTestRoutes()
	first := decode(post(h, "k1", validBody))
	rec := post(h, "k1", validBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status %d, want 200", rec.Code)
	}
	if second := decode(rec); second.ID != first.ID {
		t.Fatalf("retry created a second payment: %s vs %s", first.ID, second.ID)
	}
}

// 50 simultaneous retries of one payment must produce exactly one charge.
func TestConcurrentRetriesChargeOnce(t *testing.T) {
	h := newTestRoutes()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		ids     = map[string]bool{}
	)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := post(h, "same-key", validBody)
			p := decode(rec)
			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusCreated {
				created++
			}
			ids[p.ID] = true
		}()
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("created=%d distinct ids=%d, want 1 and 1", created, len(ids))
	}
}

// Many different payments at once must not race on shared state.
func TestConcurrentDifferentPayments(t *testing.T) {
	h := newTestRoutes()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := post(h, "key-"+strings.Repeat("x", i+1), validBody); rec.Code != http.StatusCreated {
				t.Errorf("status %d, want 201", rec.Code)
			}
		}()
	}
	wg.Wait()
}

func TestRejectsBadInput(t *testing.T) {
	cases := map[string]struct {
		key, body string
		want      int
	}{
		"no idempotency key": {"", validBody, 400},
		"not JSON":           {"k", `hello`, 400},
		"float amount":       {"k", `{"order_id":"o","amount_cents":49.99,"currency":"USD","card_number":"4111111111111111","email":"a@b.com"}`, 400},
		"negative amount":    {"k", `{"order_id":"o","amount_cents":-500,"currency":"USD","card_number":"4111111111111111","email":"a@b.com"}`, 422},
		"short card":         {"k", `{"order_id":"o","amount_cents":1,"currency":"USD","card_number":"12","email":"a@b.com"}`, 422},
		"email without @":    {"k", `{"order_id":"o","amount_cents":1,"currency":"USD","card_number":"4111111111111111","email":"sam"}`, 422},
		"oversized body":     {"k", `{"order_id":"` + strings.Repeat("x", 2<<20) + `"}`, 400},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := post(newTestRoutes(), tc.key, tc.body); rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestHealthAndReadiness(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(log, repo.NewMemory(), &http.Client{Timeout: time.Second})
	routes := h.Routes()

	get := func(path string) int {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if get("/healthz") != 200 || get("/readyz") != 200 {
		t.Fatal("healthz and readyz should both be 200 when running")
	}
	h.StartDraining()
	if get("/readyz") != 503 {
		t.Fatal("readyz should be 503 while draining")
	}
	if get("/healthz") != 200 {
		t.Fatal("healthz must stay 200 while draining, or Kubernetes would restart the pod")
	}
}

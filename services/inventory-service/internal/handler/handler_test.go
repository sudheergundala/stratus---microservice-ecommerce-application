package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stratus/inventory-service/internal/model"
	"github.com/stratus/inventory-service/internal/repo"
)

// fakeStore mimics the DynamoDB store's rules in memory: all-or-nothing
// reservations, idempotent replays, and the same errors.
type fakeStore struct {
	mu           sync.Mutex
	stock        map[string]*model.Stock
	reservations map[string][]model.Item
	fail         error
}

func newFake() *fakeStore {
	return &fakeStore{stock: map[string]*model.Stock{}, reservations: map[string][]model.Item{}}
}

func (f *fakeStore) SetStock(_ context.Context, sku string, available int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	s, ok := f.stock[sku]
	if !ok {
		s = &model.Stock{SKU: sku}
		f.stock[sku] = s
	}
	s.Available = available
	return nil
}

func (f *fakeStore) GetStock(_ context.Context, sku string) (model.Stock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return model.Stock{}, f.fail
	}
	s, ok := f.stock[sku]
	if !ok {
		return model.Stock{}, repo.ErrNotFound
	}
	return *s, nil
}

func (f *fakeStore) Reserve(_ context.Context, req model.ReservationRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return false, f.fail
	}
	if existing, ok := f.reservations[req.OrderID]; ok {
		if !model.SameItems(existing, req.Items) {
			return false, repo.ErrConflict
		}
		return false, nil
	}
	for _, it := range req.Items {
		if s, ok := f.stock[it.SKU]; !ok || s.Available < it.Quantity {
			return false, repo.ErrOutOfStock
		}
	}
	for _, it := range req.Items {
		f.stock[it.SKU].Available -= it.Quantity
		f.stock[it.SKU].Reserved += it.Quantity
	}
	f.reservations[req.OrderID] = req.Items
	return true, nil
}

func (f *fakeStore) Release(_ context.Context, orderID string) error {
	return f.finish(orderID, true)
}

func (f *fakeStore) Commit(_ context.Context, orderID string) error {
	return f.finish(orderID, false)
}

func (f *fakeStore) finish(orderID string, release bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	items, ok := f.reservations[orderID]
	if !ok {
		return repo.ErrNotFound
	}
	for _, it := range items {
		f.stock[it.SKU].Reserved -= it.Quantity
		if release {
			f.stock[it.SKU].Available += it.Quantity
		}
	}
	delete(f.reservations, orderID)
	return nil
}

func setup(t *testing.T) (*fakeStore, http.Handler) {
	t.Helper()
	store := newFake()
	_ = store.SetStock(context.Background(), "A1", 5)
	_ = store.SetStock(context.Background(), "B2", 1)
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, time.Second)
	return store, h.Routes()
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

const twoItems = `{"order_id":"ord_1","items":[{"sku":"A1","quantity":2},{"sku":"B2","quantity":1}]}`

func TestReserveMovesStockToReserved(t *testing.T) {
	store, h := setup(t)
	if rec := do(h, "POST", "/reservations", twoItems); rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body)
	}
	if a := store.stock["A1"]; a.Available != 3 || a.Reserved != 2 {
		t.Fatalf("A1 = %+v, want available 3, reserved 2", *a)
	}
}

func TestReserveIsAllOrNothing(t *testing.T) {
	store, h := setup(t)
	body := `{"order_id":"ord_1","items":[{"sku":"A1","quantity":2},{"sku":"B2","quantity":5}]}`
	if rec := do(h, "POST", "/reservations", body); rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if a := store.stock["A1"]; a.Available != 5 || a.Reserved != 0 {
		t.Fatalf("A1 changed to %+v although the reservation failed", *a)
	}
}

func TestReserveReplayAndConflict(t *testing.T) {
	_, h := setup(t)
	do(h, "POST", "/reservations", twoItems)
	if rec := do(h, "POST", "/reservations", twoItems); rec.Code != http.StatusOK {
		t.Fatalf("replay status %d, want 200", rec.Code)
	}
	other := `{"order_id":"ord_1","items":[{"sku":"A1","quantity":1}]}`
	if rec := do(h, "POST", "/reservations", other); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("conflict status %d, want 422", rec.Code)
	}
}

func TestReleaseAndCommit(t *testing.T) {
	store, h := setup(t)
	do(h, "POST", "/reservations", twoItems)
	if rec := do(h, "DELETE", "/reservations/ord_1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("release status %d, want 204", rec.Code)
	}
	if a := store.stock["A1"]; a.Available != 5 || a.Reserved != 0 {
		t.Fatalf("after release A1 = %+v, want 5/0", *a)
	}
	if rec := do(h, "DELETE", "/reservations/ord_1", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second release status %d, want 404", rec.Code)
	}

	do(h, "POST", "/reservations", `{"order_id":"ord_2","items":[{"sku":"A1","quantity":2}]}`)
	if rec := do(h, "POST", "/reservations/ord_2/commit", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("commit status %d, want 204", rec.Code)
	}
	if a := store.stock["A1"]; a.Available != 3 || a.Reserved != 0 {
		t.Fatalf("after commit A1 = %+v, want 3/0", *a)
	}
}

func TestRejectsInvalidInput(t *testing.T) {
	_, h := setup(t)
	cases := map[string]struct {
		method, path, body string
		want               int
	}{
		"malformed JSON":   {"POST", "/reservations", `{"order_id":`, 400},
		"no items":         {"POST", "/reservations", `{"order_id":"o","items":[]}`, 422},
		"zero quantity":    {"POST", "/reservations", `{"order_id":"o","items":[{"sku":"A1","quantity":0}]}`, 422},
		"duplicate sku":    {"POST", "/reservations", `{"order_id":"o","items":[{"sku":"A1","quantity":1},{"sku":"A1","quantity":1}]}`, 422},
		"bad sku":          {"POST", "/reservations", `{"order_id":"o","items":[{"sku":"A 1","quantity":1}]}`, 422},
		"negative stock":   {"PUT", "/stock/A1", `{"available":-1}`, 422},
		"unknown sku":      {"GET", "/stock/ZZ9", "", 404},
		"invalid sku path": {"GET", "/stock/bad%20sku", "", 400},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := do(h, tc.method, tc.path, tc.body); rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestStoreFailureReturns503(t *testing.T) {
	store, h := setup(t)
	store.fail = errors.New("connection refused")
	if rec := do(h, "POST", "/reservations", twoItems); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestProbes(t *testing.T) {
	store := newFake()
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, time.Second)
	routes := h.Routes()
	if do(routes, "GET", "/healthz", "").Code != 200 || do(routes, "GET", "/readyz", "").Code != 200 {
		t.Fatal("probes should be 200 while running")
	}
	h.StartDraining()
	if do(routes, "GET", "/readyz", "").Code != 503 || do(routes, "GET", "/healthz", "").Code != 200 {
		t.Fatal("while draining: readyz 503, healthz 200")
	}
}

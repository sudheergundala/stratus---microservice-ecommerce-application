// Package repo stores payments. The Repository interface lets the in-memory
// implementation be replaced by PostgreSQL without touching the handlers.
package repo

import (
	"context"
	"sync"

	"github.com/stratus/payment-service/internal/model"
)

// Repository persists payments by idempotency key.
type Repository interface {
	// CreateOnce runs create at most once per idempotency key and stores the
	// result. created reports whether this call ran create; otherwise the
	// payment stored under key is returned.
	CreateOnce(ctx context.Context, key string, create func() (model.Payment, error)) (p model.Payment, created bool, err error)
	// Get returns the payment with the given ID.
	Get(ctx context.Context, id string) (model.Payment, bool)
}

type attempt struct {
	done    chan struct{} // closed when create finishes
	payment model.Payment
	err     error
}

// Memory is an in-process Repository. Idempotency holds within one process
// only, so it must back a single replica.
type Memory struct {
	mu    sync.Mutex
	byKey map[string]*attempt
	byID  map[string]model.Payment
}

// NewMemory returns an empty in-memory repository.
func NewMemory() *Memory {
	return &Memory{byKey: map[string]*attempt{}, byID: map[string]model.Payment{}}
}

// Get implements Repository.
func (m *Memory) Get(_ context.Context, id string) (model.Payment, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	return p, ok
}

// CreateOnce implements Repository. The lock is not held while create runs
// (it calls the payment provider), so unrelated payments never queue behind
// each other. Concurrent requests with the same key wait for the first one
// and share its result.
func (m *Memory) CreateOnce(ctx context.Context, key string, create func() (model.Payment, error)) (model.Payment, bool, error) {
	m.mu.Lock()
	if a, ok := m.byKey[key]; ok {
		m.mu.Unlock()
		select {
		case <-a.done:
			return a.payment, false, a.err
		case <-ctx.Done():
			return model.Payment{}, false, ctx.Err()
		}
	}
	a := &attempt{done: make(chan struct{})}
	m.byKey[key] = a
	m.mu.Unlock()

	p, err := create()

	m.mu.Lock()
	if err != nil {
		delete(m.byKey, key) // a failed attempt may be retried with the same key
	} else {
		m.byID[p.ID] = p
	}
	a.payment, a.err = p, err
	m.mu.Unlock()
	close(a.done)
	return p, err == nil, err
}

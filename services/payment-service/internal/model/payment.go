// Package model defines the payment domain types and their validation rules.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/mail"
	"regexp"
	"time"
)

// PaymentRequest is the body of POST /payments. Amounts are integer cents.
type PaymentRequest struct {
	OrderID     string `json:"order_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
	CardNumber  string `json:"card_number"`
	Email       string `json:"email"`
}

// Payment is a captured payment. It never holds the full card number.
type Payment struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"order_id"`
	AmountCents int64     `json:"amount_cents"`
	Currency    string    `json:"currency"`
	CardLast4   string    `json:"card_last4"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

var (
	currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)
	cardRE     = regexp.MustCompile(`^[0-9]{12,19}$`)
)

// Validate rejects invalid input before any processing happens.
func (r PaymentRequest) Validate() error {
	switch {
	case r.OrderID == "":
		return errors.New("order_id is required")
	case r.AmountCents <= 0:
		return errors.New("amount_cents must be a positive integer")
	case !currencyRE.MatchString(r.Currency):
		return errors.New("currency must be a 3-letter ISO code such as USD")
	case !cardRE.MatchString(r.CardNumber):
		return errors.New("card_number must be 12 to 19 digits")
	}
	if _, err := mail.ParseAddress(r.Email); err != nil {
		return errors.New("email is not a valid address")
	}
	return nil
}

// Matches reports whether a retried request carries the same parameters as
// the payment originally created with its idempotency key.
func (r PaymentRequest) Matches(p Payment) bool {
	return r.OrderID == p.OrderID && r.AmountCents == p.AmountCents && r.Currency == p.Currency
}

// NewPayment builds a captured payment from a validated request.
func NewPayment(r PaymentRequest) Payment {
	return Payment{
		ID:          newID(),
		OrderID:     r.OrderID,
		AmountCents: r.AmountCents,
		Currency:    r.Currency,
		CardLast4:   r.CardNumber[len(r.CardNumber)-4:], // safe: Validate guarantees 12-19 digits
		Status:      "captured",
		CreatedAt:   time.Now().UTC(),
	}
}

// newID returns an unguessable, practically collision-free ID.
func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "pay_" + hex.EncodeToString(b)
}

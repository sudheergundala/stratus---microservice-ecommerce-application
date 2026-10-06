// Package model defines inventory types and their validation rules.
package model

import (
	"errors"
	"fmt"
	"regexp"
)

// MaxItemsPerReservation keeps a reservation inside one DynamoDB transaction
// (100 operations max: one per item plus the reservation record).
const MaxItemsPerReservation = 50

var skuRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Stock is the inventory level of one SKU.
type Stock struct {
	SKU       string `json:"sku"`
	Available int64  `json:"available"`
	Reserved  int64  `json:"reserved"`
}

// Item is one SKU and quantity in a reservation.
type Item struct {
	SKU      string `json:"sku"`
	Quantity int64  `json:"quantity"`
}

// ReservationRequest is the body of POST /reservations.
type ReservationRequest struct {
	OrderID string `json:"order_id"`
	Items   []Item `json:"items"`
}

// ValidSKU reports whether s is an acceptable SKU.
func ValidSKU(s string) bool { return skuRE.MatchString(s) }

// Validate rejects requests that are malformed or could not run as one
// DynamoDB transaction.
func (r ReservationRequest) Validate() error {
	if r.OrderID == "" || len(r.OrderID) > 100 {
		return errors.New("order_id is required (at most 100 characters)")
	}
	if len(r.Items) == 0 || len(r.Items) > MaxItemsPerReservation {
		return fmt.Errorf("items must contain 1 to %d entries", MaxItemsPerReservation)
	}
	seen := make(map[string]bool, len(r.Items))
	for _, it := range r.Items {
		if !ValidSKU(it.SKU) {
			return fmt.Errorf("invalid sku %q", it.SKU)
		}
		if it.Quantity < 1 || it.Quantity > 1000 {
			return fmt.Errorf("quantity for %s must be 1 to 1000", it.SKU)
		}
		// A DynamoDB transaction cannot touch the same item twice.
		if seen[it.SKU] {
			return fmt.Errorf("sku %s appears more than once; combine the quantities", it.SKU)
		}
		seen[it.SKU] = true
	}
	return nil
}

// SameItems reports whether two item lists are identical, ignoring order.
func SameItems(a, b []Item) bool {
	if len(a) != len(b) {
		return false
	}
	qty := make(map[string]int64, len(a))
	for _, it := range a {
		qty[it.SKU] = it.Quantity
	}
	for _, it := range b {
		if q, ok := qty[it.SKU]; !ok || q != it.Quantity {
			return false
		}
	}
	return true
}

// Package seller is the Seller domain: the shops that own products.
//
// It exists as its own service because ownership is the thing Product and Order
// both need, and giving it a boundary now is cheaper than extracting it later.
package seller

import (
	"errors"
	"time"
)

// Status is a shop's trading state.
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
)

// Valid reports whether s is a status the domain recognises.
func (s Status) Valid() bool {
	return s == StatusActive || s == StatusSuspended
}

// Seller is a shop.
//
// UserID refers to an account in the User domain. It is a plain string, not a
// user.User: sharing an entity across domains is what makes a "microservice"
// layout impossible to actually split.
type Seller struct {
	ID        string
	UserID    string
	ShopName  string
	Status    Status
	CreatedAt time.Time
}

var (
	ErrSellerNotFound = errors.New("seller not found")
	ErrShopNameTaken  = errors.New("shop name already registered")
	ErrAlreadySeller  = errors.New("this account already has a shop")
	ErrInvalidID      = errors.New("malformed seller id")
)

// ValidationError reports which fields were rejected and why.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "validation failed" }

func newValidationError(fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: fields}
}

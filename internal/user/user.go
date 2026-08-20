// Package user is the User domain: the entity, the business rules, and the
// ports the rules need.
//
// It imports neither net/http, nor the MongoDB driver, nor generated protobuf
// code. Everything infrastructural arrives through an interface declared here
// and implemented in an adapter. If an import of a framework or a driver
// appears in this package, the abstraction has leaked.
package user

import (
	"errors"
	"time"
)

// User is the domain entity.
//
// It carries no bson, json, or protobuf tags on purpose: each adapter defines
// its own representation and maps explicitly, so a change to the wire format
// cannot silently change what is stored.
type User struct {
	ID           string
	Name         string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

// Domain errors. Adapters match these with errors.Is and map them to their own
// vocabulary: the HTTP adapter to status codes, the gRPC adapter to codes.
// Neither mapping is any concern of this package.
var (
	ErrUserNotFound = errors.New("user not found")
	ErrEmailTaken   = errors.New("email already registered")
	ErrInvalidID    = errors.New("malformed user id")

	// ErrInvalidCredentials is deliberately one error for both an unknown
	// email and a wrong password. Two errors here would eventually become two
	// responses, and login would become a way to discover which addresses are
	// registered.
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// ValidationError reports which fields were rejected and why, so an adapter can
// render the per-field detail the API contract promises rather than a single
// opaque message.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "validation failed" }

// newValidationError returns nil when there is nothing wrong, so callers can
// return its result directly.
func newValidationError(fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: fields}
}

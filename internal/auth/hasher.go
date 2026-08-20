// Package auth implements the credential machinery the domain declares as
// ports: password hashing, and JWT issuing and verification.
//
// The domain depends on the interfaces, not on this package, so bcrypt and the
// JWT library stop here.
package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// DefaultCost is bcrypt's own default work factor, re-exported so callers do
// not have to import bcrypt just to say "the usual".
const DefaultCost = bcrypt.DefaultCost

// Hasher implements user.Hasher with bcrypt.
type Hasher struct {
	cost int
}

// NewHasher returns a Hasher. A cost outside bcrypt's accepted range falls back
// to the library default rather than failing: a mis-set cost should not stop the
// service from starting, and bcrypt's own default is a sound choice.
func NewHasher(cost int) Hasher {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	return Hasher{cost: cost}
}

func (h Hasher) Hash(plain string) (string, error) {
	sum, err := bcrypt.GenerateFromPassword([]byte(plain), h.cost)
	if err != nil {
		return "", fmt.Errorf("bcrypt hash: %w", err)
	}
	return string(sum), nil
}

// Compare reports whether plain produced hash. The error is returned as-is
// rather than inspected: the caller turns any failure into the same
// ErrInvalidCredentials, so distinguishing "wrong password" from "corrupt
// hash" here would only invite leaking the difference later.
func (h Hasher) Compare(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}

package auth

import (
	"errors"
	"time"
)

// Issuer mints tokens. Only the service that authenticates people should hold
// one; everything else gets a Verifier.
type Issuer interface {
	Issue(subject string) (token string, expiresAt time.Time, err error)
}

// NewIssuerFrom returns whichever issuer the configuration describes.
//
// A key pair means asymmetric: this service can sign, and the others can only
// check. A bare secret means HMAC, which is what the brief specifies and what a
// single-service deployment needs — everybody with the secret can both sign and
// verify, which is unremarkable when there is only one everybody.
//
// The choice is made from configuration and never from the token, which is the
// distinction that keeps algorithm confusion from being an attack.
func NewIssuerFrom(secret, privateKeyB64, publicKeyB64 string, ttl time.Duration) (Issuer, error) {
	if privateKeyB64 == "" && publicKeyB64 == "" {
		if secret == "" {
			return nil, errors.New("no JWT_SECRET and no key pair: this service cannot issue tokens")
		}
		return NewTokens(secret, ttl), nil
	}

	if privateKeyB64 == "" || publicKeyB64 == "" {
		return nil, errors.New("JWT_PRIVATE_KEY and JWT_PUBLIC_KEY must be set together")
	}
	return NewIssuer(privateKeyB64, publicKeyB64, ttl)
}

// NewVerifierFrom returns whichever verifier the configuration describes.
//
// A service that only verifies never needs the private key, and giving it one
// would undo the point of splitting them. Passing only the public key is the
// normal case for every service except the issuer.
func NewVerifierFrom(secret, publicKeyB64 string) (Verifier, error) {
	if publicKeyB64 != "" {
		return NewVerifier(publicKeyB64)
	}
	if secret == "" {
		return nil, errors.New("no JWT_SECRET and no JWT_PUBLIC_KEY: this service cannot verify tokens")
	}
	// Symmetric fallback. Note what this means: holding the secret to verify is
	// also holding the secret to sign. That is the limitation the key pair
	// exists to remove.
	return NewTokens(secret, 0), nil
}

package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken covers every reason a token was not accepted. Callers answer
// all of them with 401, so the reasons are deliberately not distinguished.
var ErrInvalidToken = errors.New("invalid token")

// signingMethod is fixed at HS256, as the brief requires.
var signingMethod = jwt.SigningMethodHS256

// Tokens issues and verifies HS256 JWTs. It implements user.TokenIssuer.
type Tokens struct {
	secret []byte
	ttl    time.Duration
}

// NewTokens returns a Tokens signing with secret for ttl.
//
// The secret has no default anywhere in this codebase: config refuses to start
// without one, so a deployment cannot accidentally sign with a known key.
func NewTokens(secret string, ttl time.Duration) *Tokens {
	return &Tokens{secret: []byte(secret), ttl: ttl}
}

// Issue returns a signed token for subject and the moment it expires.
func (t *Tokens) Issue(subject string) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(t.ttl)

	token := jwt.NewWithClaims(signingMethod, jwt.RegisteredClaims{
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	})

	signed, err := token.SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expiresAt, nil
}

// Verify checks a token and returns its subject.
//
// WithValidMethods is the important part. Without it the parser honours the
// algorithm named in the token's own header, and an attacker can present a
// token signed with "none", or an RS256 token whose "signature" is an HMAC of
// the public key. Pinning the accepted algorithm is what closes both.
func (t *Tokens) Verify(raw string) (string, error) {
	var claims jwt.RegisteredClaims

	_, err := jwt.ParseWithClaims(raw, &claims,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{signingMethod.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidToken, err)
	}

	if claims.Subject == "" {
		return "", fmt.Errorf("%w: no subject", ErrInvalidToken)
	}
	return claims.Subject, nil
}

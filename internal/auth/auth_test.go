package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/DoIttikorn/e-commerce/internal/auth"
)

const testSecret = "a-test-secret-of-at-least-32-characters"

func TestIssueThenVerifyRoundTrip(t *testing.T) {
	tokens := auth.NewTokens(testSecret, time.Hour)

	raw, expiresAt, err := tokens.Issue("user-1")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Error("ExpiresAt is not in the future")
	}

	subject, err := tokens.Verify(raw)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if subject != "user-1" {
		t.Errorf("subject = %q, want %q", subject, "user-1")
	}
}

func TestVerifyRejectsTamperedAndForeignTokens(t *testing.T) {
	tokens := auth.NewTokens(testSecret, time.Hour)
	valid, _, err := tokens.Issue("user-1")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"not a jwt", "clearly-not-a-token"},
		{"signature altered", valid[:len(valid)-4] + "AAAA"},
		{"signed with another secret", signedWith(t, jwt.SigningMethodHS256, "a-completely-different-secret-value", "user-1", time.Hour)},
		// Method pinning, not merely rejecting "none": a token signed with a
		// different HMAC strength must fail too.
		{"different HMAC algorithm", signedWith(t, jwt.SigningMethodHS512, testSecret, "user-1", time.Hour)},
		{"expired", signedWith(t, jwt.SigningMethodHS256, testSecret, "user-1", -time.Hour)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tokens.Verify(tt.raw); !errors.Is(err, auth.ErrInvalidToken) {
				t.Errorf("Verify() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

// The classic JWT bypass: a token declaring alg "none" and carrying no
// signature. A parser that honours the token's own header accepts it.
func TestVerifyRejectsAlgNone(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{
		Subject:   "attacker",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	raw, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("building the unsigned token failed: %v", err)
	}

	tokens := auth.NewTokens(testSecret, time.Hour)

	if subject, err := tokens.Verify(raw); err == nil {
		t.Fatalf("Verify() accepted an unsigned token and returned subject %q", subject)
	}
}

// A token with a valid signature but no subject identifies nobody.
func TestVerifyRejectsMissingSubject(t *testing.T) {
	raw := signedWith(t, jwt.SigningMethodHS256, testSecret, "", time.Hour)

	if _, err := auth.NewTokens(testSecret, time.Hour).Verify(raw); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("Verify() error = %v, want ErrInvalidToken", err)
	}
}

func TestHasherRoundTrip(t *testing.T) {
	// MinCost keeps the suite fast; production cost comes from the caller.
	h := auth.NewHasher(4)

	hash, err := h.Hash("correct-horse-battery")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if hash == "correct-horse-battery" {
		t.Fatal("Hash returned the plaintext")
	}
	if err := h.Compare(hash, "correct-horse-battery"); err != nil {
		t.Errorf("Compare() with the right password error = %v", err)
	}
	if err := h.Compare(hash, "the-wrong-password"); err == nil {
		t.Error("Compare() accepted the wrong password")
	}
}

// bcrypt salts, so the same password must not produce the same hash twice.
func TestHasherSalts(t *testing.T) {
	h := auth.NewHasher(4)

	first, _ := h.Hash("same-password")
	second, _ := h.Hash("same-password")

	if first == second {
		t.Error("two hashes of the same password are identical; the salt is missing")
	}
}

type fakeVerifier struct {
	subject string
	err     error
}

func (f fakeVerifier) Verify(string) (string, error) { return f.subject, f.err }

func TestMiddlewareAcceptsValidToken(t *testing.T) {
	var gotSubject string
	h := auth.Middleware(fakeVerifier{subject: "user-9"})(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			gotSubject, _ = auth.SubjectFrom(r.Context())
		}))

	rec := serveWithAuth(h, "Bearer good-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if gotSubject != "user-9" {
		t.Errorf("subject in context = %q, want %q", gotSubject, "user-9")
	}
}

func TestMiddlewareRejectsBadAuthorization(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		verifier fakeVerifier
	}{
		{"no header", "", fakeVerifier{subject: "user-9"}},
		{"wrong scheme", "Basic dXNlcjpwYXNz", fakeVerifier{subject: "user-9"}},
		{"bearer with no token", "Bearer ", fakeVerifier{subject: "user-9"}},
		{"token rejected", "Bearer bad", fakeVerifier{err: auth.ErrInvalidToken}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := auth.Middleware(tt.verifier)(
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

			rec := serveWithAuth(h, tt.header)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if called {
				t.Error("the protected handler ran despite the request being rejected")
			}
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 sent without a WWW-Authenticate header")
			}
		})
	}
}

// The scheme is a case-insensitive token per RFC 7235.
func TestMiddlewareAcceptsAnyBearerCasing(t *testing.T) {
	for _, header := range []string{"Bearer t", "bearer t", "BEARER t"} {
		t.Run(header, func(t *testing.T) {
			h := auth.Middleware(fakeVerifier{subject: "u"})(
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

			if rec := serveWithAuth(h, header); rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}
}

func serveWithAuth(h http.Handler, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func signedWith(t *testing.T, method jwt.SigningMethod, secret, subject string, ttl time.Duration) string {
	t.Helper()

	claims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
	}
	if subject != "" {
		claims.Subject = subject
	}

	raw, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("signing the test token failed: %v", err)
	}
	return raw
}

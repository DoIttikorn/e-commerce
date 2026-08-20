package auth

import (
	"context"
	"net/http"
	"strings"
)

type subjectKey struct{}

// WithSubject returns a context carrying the authenticated user's ID.
func WithSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, subjectKey{}, subject)
}

// SubjectFrom returns the authenticated user's ID, and whether there was one.
func SubjectFrom(ctx context.Context) (string, bool) {
	subject, ok := ctx.Value(subjectKey{}).(string)
	return subject, ok && subject != ""
}

// Verifier is the slice of Tokens the middleware needs. Declaring it here, at
// the consumer, keeps the middleware testable with a two-line fake.
type Verifier interface {
	Verify(raw string) (subject string, err error)
}

// Middleware rejects any request without a valid bearer token.
//
// Written in the stdlib func(http.Handler) http.Handler shape so it survives a
// change of web framework, like everything in internal/middleware.
func Middleware(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := BearerToken(r.Header.Get("Authorization"))
			if !ok {
				unauthorized(w)
				return
			}

			subject, err := v.Verify(raw)
			if err != nil {
				// Deliberately not logged or reported in detail: the client
				// learns only that the token was not accepted.
				unauthorized(w)
				return
			}

			next.ServeHTTP(w, r.WithContext(WithSubject(r.Context(), subject)))
		})
	}
}

// BearerToken extracts the credential from an Authorization value. The scheme
// is matched case-insensitively, as RFC 7235 requires.
//
// Exported because gRPC carries the same value in metadata rather than a
// header: where the string comes from is each adapter's business, but the
// scheme parsing is one rule and belongs in one place.
func BearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}

	token = strings.TrimSpace(token)
	return token, token != ""
}

func unauthorized(w http.ResponseWriter) {
	// WWW-Authenticate is what makes a 401 a correct 401 rather than a 403
	// wearing the wrong number.
	w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

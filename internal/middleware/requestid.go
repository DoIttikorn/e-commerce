package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/logging"
)

// RequestIDHeader is both read and written, so an ID assigned by an upstream
// gateway survives into this service's logs instead of being replaced — which
// is the whole point when tracing a request across several services.
const RequestIDHeader = "X-Request-ID"

// maxRequestIDLen bounds an attacker-controlled value before it reaches the logs.
const maxRequestIDLen = 64

// RequestID attaches a correlation ID to every request, reusing the inbound
// header when it carries a usable one and generating a fresh ID otherwise.
//
// Register it first: everything downstream that logs with the request context
// gets the ID for free, and middleware ahead of it would log without one.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := sanitizeRequestID(r.Header.Get(RequestIDHeader))
			if id == "" {
				id = newRequestID()
			}

			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
		})
	}
}

func newRequestID() string {
	var b [8]byte
	// crypto/rand.Read never returns an error; it panics on an unusable source.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// sanitizeRequestID rejects anything that is not short, printable ASCII.
// The inbound header is client-controlled and ends up in log records, so an
// unbounded or control-character-laden value is a log-forging vector.
func sanitizeRequestID(s string) string {
	if s == "" || len(s) > maxRequestIDLen {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e {
			return ""
		}
	}
	return s
}

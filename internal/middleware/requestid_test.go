package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DoIttikorn/e-commerce/internal/logging"
	"github.com/DoIttikorn/e-commerce/internal/middleware"
)

// captureID runs one request through the middleware and reports the ID the
// handler saw and the ID returned to the client.
func captureID(t *testing.T, inbound string) (seen, returned string) {
	t.Helper()

	h := middleware.RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = logging.RequestIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if inbound != "" {
		req.Header.Set(middleware.RequestIDHeader, inbound)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return seen, rec.Header().Get(middleware.RequestIDHeader)
}

func TestGeneratesIDWhenAbsent(t *testing.T) {
	seen, returned := captureID(t, "")

	if seen == "" {
		t.Error("handler saw no request ID")
	}
	if seen != returned {
		t.Errorf("handler saw %q but client got %q", seen, returned)
	}
}

// An ID set by an upstream gateway must survive, or a request cannot be
// followed across service boundaries.
func TestReusesInboundID(t *testing.T) {
	const upstream = "gateway-abc-123"

	seen, returned := captureID(t, upstream)

	if seen != upstream {
		t.Errorf("handler saw %q, want the inbound %q", seen, upstream)
	}
	if returned != upstream {
		t.Errorf("client got %q, want the inbound %q", returned, upstream)
	}
}

// The header is client-controlled and lands in log records, so hostile values
// must be replaced rather than trusted.
func TestRejectsUnusableInboundID(t *testing.T) {
	tests := []struct {
		name    string
		inbound string
	}{
		{"newline", "abc\ninjected-log-line"},
		{"carriage return", "abc\rdef"},
		{"control byte", "abc\x00def"},
		{"non-ascii", "abcédef"},
		{"too long", strings.Repeat("a", 65)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen, _ := captureID(t, tt.inbound)

			if seen == tt.inbound {
				t.Errorf("hostile inbound ID %q was used as-is", tt.inbound)
			}
			if seen == "" {
				t.Error("no replacement ID was generated")
			}
		})
	}
}

// The boundary case on the other side of the length limit must still be kept.
func TestKeepsInboundIDAtMaxLength(t *testing.T) {
	inbound := strings.Repeat("a", 64)

	seen, _ := captureID(t, inbound)

	if seen != inbound {
		t.Errorf("a 64-character ID was replaced; the limit is off by one")
	}
}

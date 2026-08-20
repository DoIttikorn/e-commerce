package router_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DoIttikorn/e-commerce/internal/router"
)

// Two middlewares both want the matched pattern — tracing to name its span,
// metrics to label its series. A second holder would shadow the first, and the
// outer middleware would read "" for every request without any way to tell that
// apart from a genuine miss.
func TestPatternCaptureIsSharedBetweenMiddlewares(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/users/1", nil)

	outer := router.WithPatternCapture(req)
	inner := router.WithPatternCapture(outer)

	// The router matches once and sets the pattern once, on the innermost
	// request it was given.
	router.SetPattern(inner, "/users/{id}")

	if got := router.PatternFrom(outer); got != "/users/{id}" {
		t.Errorf("outer middleware read %q, want the matched pattern", got)
	}
	if got := router.PatternFrom(inner); got != "/users/{id}" {
		t.Errorf("inner middleware read %q, want the matched pattern", got)
	}
}

// A request nobody prepared reports no pattern rather than panicking.
func TestPatternFromAnUnpreparedRequestIsEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/users/1", nil)

	router.SetPattern(req, "/users/{id}") // must be a no-op, not a panic

	if got := router.PatternFrom(req); got != "" {
		t.Errorf("PatternFrom() = %q, want empty", got)
	}
}

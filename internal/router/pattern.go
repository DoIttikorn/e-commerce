package router

import (
	"context"
	"net/http"
)

type patternKey struct{}

// pattern is a mutable holder rather than a plain context value on purpose.
//
// Middleware registered with Use runs before the route is matched, so the
// pattern is only known further down the chain — and a context value set there
// travels no further than the handler, never back out to the middleware that
// needs it. A pointer placed on the way in and filled on the way down solves
// that without the router knowing who wants the value.
type pattern struct{ value string }

// WithPatternCapture prepares r to receive its matched route pattern. Call it
// from middleware that needs the pattern after the handler has run.
func WithPatternCapture(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), patternKey{}, &pattern{}))
}

// SetPattern records the matched route pattern on r. Adapters call it once the
// route is known; it does nothing if r was not prepared by WithPatternCapture.
func SetPattern(r *http.Request, p string) {
	if holder, ok := r.Context().Value(patternKey{}).(*pattern); ok {
		holder.value = p
	}
}

// PatternFrom returns the matched route pattern, or "" when the request matched
// no route or was never prepared for capture.
func PatternFrom(r *http.Request) string {
	holder, ok := r.Context().Value(patternKey{}).(*pattern)
	if !ok {
		return ""
	}
	return holder.value
}

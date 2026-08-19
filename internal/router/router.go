// Package router defines the HTTP routing port.
//
// Handlers registered through a Router are plain http.HandlerFunc and read
// path parameters with the standard library's r.PathValue. No handler imports
// a web framework, so switching from one net/http based framework to another
// means writing a single adapter rather than touching handler code.
//
// Route patterns use the {name} wildcard form, which chi and the standard
// library's ServeMux share. An adapter for a framework with different syntax
// (echo's :name, for example) translates when registering the route.
//
// Keep this interface small. Every method added here is a method every future
// adapter has to implement.
package router

import "net/http"

// Router registers routes and middleware, and produces the http.Handler that
// serves them.
type Router interface {
	// Handle registers h for method and pattern.
	Handle(method, pattern string, h http.HandlerFunc)

	// Group registers routes under prefix with their own middleware stack.
	Group(prefix string, fn func(r Router))

	// Use appends middleware in the standard func(http.Handler) http.Handler
	// shape, which chi accepts natively and echo accepts via WrapMiddleware.
	Use(mw ...func(http.Handler) http.Handler)

	// Handler returns the composed handler, for http.Server and httptest.
	Handler() http.Handler
}

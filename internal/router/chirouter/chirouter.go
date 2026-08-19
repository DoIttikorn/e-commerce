// Package chirouter adapts go-chi to the router.Router port.
//
// It is named chirouter rather than chi so that it can import the chi package
// it wraps.
package chirouter

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/DoIttikorn/e-commerce/internal/router"
)

// Router implements router.Router on top of chi.
type Router struct {
	mux chi.Router

	// prefix accumulates the enclosing Group prefixes so a route can report its
	// full pattern, which is what metrics and tracing need as a label.
	prefix string
}

var _ router.Router = (*Router)(nil)

// New returns a Router backed by a fresh chi mux.
func New() *Router {
	return &Router{mux: chi.NewRouter()}
}

func (rt *Router) Handle(method, pattern string, h http.HandlerFunc) {
	rt.mux.Method(method, pattern, rt.wrap(rt.prefix+pattern, h))
}

func (rt *Router) Group(prefix string, fn func(router.Router)) {
	rt.mux.Route(prefix, func(sub chi.Router) {
		fn(&Router{mux: sub, prefix: rt.prefix + prefix})
	})
}

func (rt *Router) Use(mw ...func(http.Handler) http.Handler) {
	rt.mux.Use(mw...)
}

func (rt *Router) Handler() http.Handler {
	return rt.mux
}

// wrap normalises what the rest of the application sees: the matched pattern
// and the captured parameters, both through router and net/http rather than
// through chi, so nothing downstream imports a web framework.
func (rt *Router) wrap(fullPattern string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.SetPattern(r, fullPattern)

		// The "*" key is chi's catch-all rather than a named parameter.
		if rc := chi.RouteContext(r.Context()); rc != nil {
			for i, key := range rc.URLParams.Keys {
				if key == "*" {
					continue
				}
				r.SetPathValue(key, rc.URLParams.Values[i])
			}
		}
		h(w, r)
	})
}

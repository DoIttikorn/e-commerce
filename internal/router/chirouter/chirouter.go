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
}

var _ router.Router = (*Router)(nil)

// New returns a Router backed by a fresh chi mux.
func New() *Router {
	return &Router{mux: chi.NewRouter()}
}

func (rt *Router) Handle(method, pattern string, h http.HandlerFunc) {
	rt.mux.Method(method, pattern, exposePathValues(h))
}

func (rt *Router) Group(prefix string, fn func(router.Router)) {
	rt.mux.Route(prefix, func(sub chi.Router) {
		fn(&Router{mux: sub})
	})
}

func (rt *Router) Use(mw ...func(http.Handler) http.Handler) {
	rt.mux.Use(mw...)
}

func (rt *Router) Handler() http.Handler {
	return rt.mux
}

// exposePathValues copies the parameters chi captured onto the request, so
// handlers read them with r.PathValue and never import chi. The "*" key is
// chi's catch-all rather than a named parameter, so it is skipped.
func exposePathValues(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// Package admin serves the operator surface: metrics and profiling.
//
// It is a separate handler from the API so it can be bound to its own port and
// kept off the public network. That separation is not cosmetic — pprof exposes
// process memory and lets a caller stall the process for the length of a
// profile, so it must never be reachable from the internet.
package admin

import (
	"net/http"
	"net/http/pprof"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewHandler returns the admin mux: Prometheus metrics and Go's profiling
// endpoints.
func NewHandler(gatherer prometheus.Gatherer) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))

	// Registered by hand rather than by importing net/http/pprof for its side
	// effect, which attaches these to http.DefaultServeMux — where any handler
	// that happens to use the default mux would start serving them too.
	// pprof.Index also covers /debug/pprof/heap, /goroutine, /allocs and so on.
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	return mux
}

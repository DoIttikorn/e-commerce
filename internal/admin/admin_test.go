package admin_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/DoIttikorn/e-commerce/internal/admin"
)

func TestServesMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{
		Name: "canary_total",
		Help: "A metric that must appear in the exposition output.",
	}))

	rec := do(admin.NewHandler(reg), "/metrics")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "canary_total") {
		t.Error("registered metric missing from /metrics output")
	}
}

// pprof is the profiler the performance work depends on, so its routes are
// worth pinning: a stray change to the mux patterns would silently 404 them.
func TestServesPprof(t *testing.T) {
	h := admin.NewHandler(prometheus.NewRegistry())

	// /profile and /trace are excluded on purpose: both block for their whole
	// sampling window, which would stall the test suite.
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/goroutine", "/debug/pprof/cmdline"} {
		t.Run(path, func(t *testing.T) {
			if rec := do(h, path); rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}
}

func do(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

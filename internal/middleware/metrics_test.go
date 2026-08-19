package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DoIttikorn/e-commerce/internal/middleware"
	"github.com/DoIttikorn/e-commerce/internal/router"
	"github.com/DoIttikorn/e-commerce/internal/router/chirouter"
)

func newMetered(t *testing.T) (*prometheus.Registry, router.Router) {
	t.Helper()

	reg := prometheus.NewRegistry()
	m := middleware.NewMetrics(reg)

	r := chirouter.New()
	r.Use(m.Middleware())
	return reg, r
}

func get(r router.Router, target string) {
	r.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
}

// This is the test that matters. Labelling by r.URL.Path would give every user
// ID its own time series; unbounded label cardinality is the standard way a
// Prometheus install gets taken down by the service it monitors.
func TestRouteLabelUsesPatternNotPath(t *testing.T) {
	reg, r := newMetered(t)
	r.Handle(http.MethodGet, "/users/{id}", func(http.ResponseWriter, *http.Request) {})

	for _, id := range []string{"1", "2", "3", "4", "5"} {
		get(r, "/users/"+id)
	}

	if got := seriesCount(t, reg, "http_requests_total"); got != 1 {
		t.Errorf("http_requests_total series = %d, want 1 for five distinct URLs", got)
	}
	if got := routeLabels(t, reg); len(got) != 1 || got[0] != "/users/{id}" {
		t.Errorf("route labels = %q, want [\"/users/{id}\"]", got)
	}
}

// Requests that match nothing must collapse to a single series too, or a
// scanner probing random paths becomes a cardinality attack.
func TestUnmatchedRequestsShareOneSeries(t *testing.T) {
	reg, r := newMetered(t)
	r.Handle(http.MethodGet, "/known", func(http.ResponseWriter, *http.Request) {})

	for _, p := range []string{"/nope-1", "/nope-2", "/wp-admin", "/.env"} {
		get(r, p)
	}

	if got := seriesCount(t, reg, "http_requests_total"); got != 1 {
		t.Errorf("http_requests_total series = %d, want 1", got)
	}
	if got := routeLabels(t, reg); len(got) != 1 || got[0] != "unmatched" {
		t.Errorf("route labels = %q, want [\"unmatched\"]", got)
	}
}

// A group prefix belongs in the label: /users/{id} under /api/v1 and under a
// future /api/v2 must not be merged into one series.
func TestRouteLabelIncludesGroupPrefix(t *testing.T) {
	reg, r := newMetered(t)
	r.Group("/api/v1", func(sub router.Router) {
		sub.Handle(http.MethodGet, "/users/{id}", func(http.ResponseWriter, *http.Request) {})
	})

	get(r, "/api/v1/users/42")

	if got := routeLabels(t, reg); len(got) != 1 || got[0] != "/api/v1/users/{id}" {
		t.Errorf("route labels = %q, want [\"/api/v1/users/{id}\"]", got)
	}
}

func TestRecordsStatusAndDuration(t *testing.T) {
	reg, r := newMetered(t)
	r.Handle(http.MethodGet, "/teapot", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	get(r, "/teapot")

	if got := labelValues(t, reg, "http_requests_total", "status"); len(got) != 1 || got[0] != "418" {
		t.Errorf("status labels = %q, want [\"418\"]", got)
	}
	if got := seriesCount(t, reg, "http_request_duration_seconds"); got != 1 {
		t.Errorf("duration series = %d, want 1", got)
	}
}

func seriesCount(t *testing.T, reg *prometheus.Registry, metric string) int {
	t.Helper()

	n, err := testutil.GatherAndCount(reg, metric)
	if err != nil {
		t.Fatalf("GatherAndCount(%q) error = %v", metric, err)
	}
	return n
}

func routeLabels(t *testing.T, reg *prometheus.Registry) []string {
	t.Helper()
	return labelValues(t, reg, "http_requests_total", "route")
}

func labelValues(t *testing.T, reg *prometheus.Registry, metric, label string) []string {
	t.Helper()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}

	var out []string
	for _, f := range families {
		if f.GetName() != metric {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label {
					out = append(out, l.GetValue())
				}
			}
		}
	}
	return out
}

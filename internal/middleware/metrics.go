package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/DoIttikorn/e-commerce/internal/router"
)

// unmatchedRoute labels requests that matched no route. Without it, every 404
// from a scanner probing random URLs would mint a new time series.
const unmatchedRoute = "unmatched"

// Metrics records the RED signals — rate, errors, duration — for HTTP traffic.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

// NewMetrics registers the HTTP collectors on reg.
//
// Taking a Registerer rather than using the package-level default keeps the
// collectors out of global state and lets tests use a throwaway registry.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by route, method and status code.",
		}, []string{"method", "route", "status"}),

		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds by route and method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),

		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "HTTP requests currently being served.",
		}),
	}

	reg.MustRegister(m.requests, m.duration, m.inFlight)
	return m
}

// Middleware measures every request. Register it after RequestID so failures it
// records can be correlated with the log line for the same request.
func (m *Metrics) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.inFlight.Inc()
			defer m.inFlight.Dec()

			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			r = router.WithPatternCapture(r)

			next.ServeHTTP(rec, r)

			// Label by route pattern, never by r.URL.Path: a raw path gives
			// /users/{id} one series per user ID, and unbounded label
			// cardinality is the standard way to take Prometheus down.
			route := router.PatternFrom(r)
			if route == "" {
				route = unmatchedRoute
			}

			m.requests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
			m.duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		})
	}
}

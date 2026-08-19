package chirouter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DoIttikorn/user-management-api/internal/router"
	"github.com/DoIttikorn/user-management-api/internal/router/chirouter"
)

// The point of these tests is the port's contract, not chi. They are written
// against router.Router so they can be reused verbatim to verify any future
// adapter behaves identically.

func TestHandleRoutesByMethodAndPattern(t *testing.T) {
	var r router.Router = chirouter.New()
	r.Handle(http.MethodGet, "/users", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("list"))
	})

	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantBody   string
	}{
		{"match", http.MethodGet, "/users", http.StatusOK, "list"},
		{"wrong method", http.MethodPost, "/users", http.StatusMethodNotAllowed, ""},
		{"unknown path", http.MethodGet, "/missing", http.StatusNotFound, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(r, tt.method, tt.target)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

// TestPathValueIsPopulated is the test that matters: it proves a handler can
// read route parameters through the standard library instead of chi.URLParam,
// which is what keeps handler code framework-free.
func TestPathValueIsPopulated(t *testing.T) {
	var r router.Router = chirouter.New()
	r.Handle(http.MethodGet, "/users/{id}/orders/{orderID}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(req.PathValue("id") + ":" + req.PathValue("orderID")))
	})

	rec := serve(r, http.MethodGet, "/users/u-42/orders/o-7")

	if got, want := rec.Body.String(), "u-42:o-7"; got != want {
		t.Errorf("path values = %q, want %q", got, want)
	}
}

func TestGroupAppliesPrefix(t *testing.T) {
	var r router.Router = chirouter.New()
	r.Group("/api/v1", func(sub router.Router) {
		sub.Handle(http.MethodGet, "/users/{id}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = w.Write([]byte(req.PathValue("id")))
		})
	})

	rec := serve(r, http.MethodGet, "/api/v1/users/abc")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "abc"; got != want {
		t.Errorf("path value = %q, want %q", got, want)
	}
}

func TestUseRunsMiddlewareInOrder(t *testing.T) {
	var order []string

	var r router.Router = chirouter.New()
	r.Use(
		record(&order, "first"),
		record(&order, "second"),
	)
	r.Handle(http.MethodGet, "/", func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	})

	serve(r, http.MethodGet, "/")

	want := []string{"first", "second", "handler"}
	if len(order) != len(want) {
		t.Fatalf("call order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("call order = %v, want %v", order, want)
		}
	}
}

func record(into *[]string, name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*into = append(*into, name)
			next.ServeHTTP(w, r)
		})
	}
}

func serve(r router.Router, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

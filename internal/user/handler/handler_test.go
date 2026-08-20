package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/router"
	"github.com/DoIttikorn/e-commerce/internal/router/chirouter"
	"github.com/DoIttikorn/e-commerce/internal/user"
	"github.com/DoIttikorn/e-commerce/internal/user/handler"
)

const (
	callerID  = "caller-id"
	otherID   = "somebody-else"
	leakyHash = "$2a$10$this-must-never-appear-in-a-response"
)

type fakeService struct {
	registerFn func(context.Context, user.NewUser) (user.User, error)
	authFn     func(context.Context, string, string) (user.Token, error)
	byIDFn     func(context.Context, string) (user.User, error)
	listFn     func(context.Context, int, int) ([]user.User, int, error)
	updateFn   func(context.Context, string, user.Update) (user.User, error)
	deleteFn   func(context.Context, string) error

	gotUpdate user.Update
	gotLimit  int
	gotOffset int
}

func (f *fakeService) Register(ctx context.Context, in user.NewUser) (user.User, error) {
	if f.registerFn != nil {
		return f.registerFn(ctx, in)
	}
	return user.User{ID: "new-id", Name: in.Name, Email: in.Email, CreatedAt: time.Now()}, nil
}

func (f *fakeService) Authenticate(ctx context.Context, email, password string) (user.Token, error) {
	if f.authFn != nil {
		return f.authFn(ctx, email, password)
	}
	return user.Token{Value: "a-token", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (f *fakeService) ByID(ctx context.Context, id string) (user.User, error) {
	if f.byIDFn != nil {
		return f.byIDFn(ctx, id)
	}
	return user.User{ID: id, Name: "N", Email: "n@example.com"}, nil
}

func (f *fakeService) List(ctx context.Context, limit, offset int) ([]user.User, int, error) {
	f.gotLimit, f.gotOffset = limit, offset
	if f.listFn != nil {
		return f.listFn(ctx, limit, offset)
	}
	return []user.User{{ID: "1", Name: "N", Email: "n@example.com"}}, 1, nil
}

func (f *fakeService) Update(ctx context.Context, id string, upd user.Update) (user.User, error) {
	f.gotUpdate = upd
	if f.updateFn != nil {
		return f.updateFn(ctx, id, upd)
	}
	return user.User{ID: id, Name: "N", Email: "n@example.com"}, nil
}

func (f *fakeService) Delete(ctx context.Context, id string) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id)
	}
	return nil
}

// newAPI mounts the routes with a stand-in for the auth middleware, so these
// tests exercise the handlers rather than re-testing JWT verification.
// authenticated=false stands for a request with no usable token.
func newAPI(svc handler.Service, authenticated bool) router.Router {
	r := chirouter.New()
	stub := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if !authenticated {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			next.ServeHTTP(w, req.WithContext(auth.WithSubject(req.Context(), callerID)))
		})
	}
	handler.New(svc, slog.New(slog.DiscardHandler)).Register(r, stub)
	return r
}

func do(r router.Router, method, target, body string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	return rec
}

func TestRegisterCreatesUser(t *testing.T) {
	rec := do(newAPI(&fakeService{}, false), http.MethodPost, "/api/v1/auth/register",
		`{"name":"Ittikorn","email":"i@example.com","password":"correct-horse-battery"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusCreated, rec.Body)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	for _, key := range []string{"id", "name", "email", "created_at"} {
		if _, ok := got[key]; !ok {
			t.Errorf("response is missing %q: %v", key, got)
		}
	}
}

// The single most important assertion in this file.
func TestPasswordHashNeverAppearsInAnyResponse(t *testing.T) {
	svc := &fakeService{
		registerFn: func(_ context.Context, in user.NewUser) (user.User, error) {
			return user.User{ID: "1", Name: in.Name, Email: in.Email, PasswordHash: leakyHash}, nil
		},
		byIDFn: func(_ context.Context, id string) (user.User, error) {
			return user.User{ID: id, PasswordHash: leakyHash}, nil
		},
		listFn: func(context.Context, int, int) ([]user.User, int, error) {
			return []user.User{{ID: "1", PasswordHash: leakyHash}}, 1, nil
		},
		updateFn: func(_ context.Context, id string, _ user.Update) (user.User, error) {
			return user.User{ID: id, PasswordHash: leakyHash}, nil
		},
	}

	responses := []*httptest.ResponseRecorder{
		do(newAPI(svc, false), http.MethodPost, "/api/v1/auth/register", `{"name":"A","email":"a@b.com","password":"correct-horse-battery"}`),
		do(newAPI(svc, true), http.MethodGet, "/api/v1/users/"+callerID, ""),
		do(newAPI(svc, true), http.MethodGet, "/api/v1/users", ""),
		do(newAPI(svc, true), http.MethodPatch, "/api/v1/users/"+callerID, `{"name":"B"}`),
	}

	for i, rec := range responses {
		body := rec.Body.String()
		if strings.Contains(body, leakyHash) || strings.Contains(body, "password") {
			t.Errorf("response %d leaked password material: %s", i, body)
		}
	}
}

func TestRegisterErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		svcErr     error
		wantStatus int
		wantError  string
	}{
		{
			name:       "validation failure",
			body:       `{"name":"","email":"nope","password":"x"}`,
			svcErr:     &user.ValidationError{Fields: map[string]string{"email": "must be a valid email address"}},
			wantStatus: http.StatusBadRequest,
			wantError:  "validation failed",
		},
		{
			name:       "duplicate email",
			body:       `{"name":"A","email":"a@b.com","password":"correct-horse-battery"}`,
			svcErr:     user.ErrEmailTaken,
			wantStatus: http.StatusConflict,
			wantError:  "email already registered",
		},
		{
			name:       "malformed json is 400 not 500",
			body:       `{"name": "broken",`,
			wantStatus: http.StatusBadRequest,
			wantError:  "malformed json body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{registerFn: func(context.Context, user.NewUser) (user.User, error) {
				return user.User{}, tt.svcErr
			}}

			rec := do(newAPI(svc, false), http.MethodPost, "/api/v1/auth/register", tt.body)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := decodeError(t, rec); got.Error != tt.wantError {
				t.Errorf("error = %q, want %q", got.Error, tt.wantError)
			}
		})
	}
}

// Validation failures must name the fields, per the API contract.
func TestValidationFailureReportsFields(t *testing.T) {
	svc := &fakeService{registerFn: func(context.Context, user.NewUser) (user.User, error) {
		return user.User{}, &user.ValidationError{Fields: map[string]string{
			"name":  "is required",
			"email": "must be a valid email address",
		}}
	}}

	rec := do(newAPI(svc, false), http.MethodPost, "/api/v1/auth/register", `{}`)

	got := decodeError(t, rec)
	if got.Fields["email"] == "" || got.Fields["name"] == "" {
		t.Errorf("fields = %v, want entries for name and email", got.Fields)
	}
}

func TestLogin(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		rec := do(newAPI(&fakeService{}, false), http.MethodPost, "/api/v1/auth/login",
			`{"email":"a@b.com","password":"correct-horse-battery"}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got["token"] == "" || got["expires_at"] == nil {
			t.Errorf("response = %v, want token and expires_at", got)
		}
	})

	t.Run("bad credentials are 401", func(t *testing.T) {
		svc := &fakeService{authFn: func(context.Context, string, string) (user.Token, error) {
			return user.Token{}, user.ErrInvalidCredentials
		}}

		rec := do(newAPI(svc, false), http.MethodPost, "/api/v1/auth/login", `{"email":"a@b.com","password":"wrong"}`)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
}

// Every /users route must be unreachable without a token.
func TestProtectedRoutesRejectAnonymous(t *testing.T) {
	r := newAPI(&fakeService{}, false)

	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodGet, "/api/v1/users/" + callerID},
		{http.MethodPatch, "/api/v1/users/" + callerID},
		{http.MethodDelete, "/api/v1/users/" + callerID},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			if rec := do(r, tc.method, tc.target, `{}`); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestGetUserErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		svcErr     error
		wantStatus int
	}{
		{"missing", user.ErrUserNotFound, http.StatusNotFound},
		{"malformed id", user.ErrInvalidID, http.StatusBadRequest},
		{"unexpected", errors.New("mongo exploded"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{byIDFn: func(context.Context, string) (user.User, error) {
				return user.User{}, tt.svcErr
			}}

			rec := do(newAPI(svc, true), http.MethodGet, "/api/v1/users/x", "")

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			// An internal failure must not describe itself to the client.
			if strings.Contains(rec.Body.String(), "mongo exploded") {
				t.Errorf("internal error leaked to the client: %s", rec.Body)
			}
		})
	}
}

func TestListAppliesAndReportsPaging(t *testing.T) {
	svc := &fakeService{}

	rec := do(newAPI(svc, true), http.MethodGet, "/api/v1/users?limit=5&offset=10", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if svc.gotLimit != 5 || svc.gotOffset != 10 {
		t.Errorf("service got limit=%d offset=%d, want 5 and 10", svc.gotLimit, svc.gotOffset)
	}

	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["total"] == nil || got["limit"] == nil || got["offset"] == nil {
		t.Errorf("response = %v, want total, limit and offset", got)
	}
}

// An absurd limit must be clamped rather than passed through.
func TestListClampsHostileLimit(t *testing.T) {
	svc := &fakeService{}

	do(newAPI(svc, true), http.MethodGet, "/api/v1/users?limit=999999", "")

	if svc.gotLimit != user.MaxPageSize {
		t.Errorf("service got limit=%d, want it clamped to %d", svc.gotLimit, user.MaxPageSize)
	}
}

// PATCH semantics survive the wire: an omitted key must arrive as nil.
func TestUpdateSendsOnlySuppliedFields(t *testing.T) {
	svc := &fakeService{}

	rec := do(newAPI(svc, true), http.MethodPatch, "/api/v1/users/"+callerID, `{"name":"Renamed"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body)
	}
	if svc.gotUpdate.Email != nil {
		t.Error("Email reached the service as non-nil despite being omitted")
	}
	if svc.gotUpdate.Name == nil || *svc.gotUpdate.Name != "Renamed" {
		t.Errorf("Name = %v, want \"Renamed\"", svc.gotUpdate.Name)
	}
}

func TestDeleteReturnsNoContent(t *testing.T) {
	rec := do(newAPI(&fakeService{}, true), http.MethodDelete, "/api/v1/users/"+callerID, "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 carried a body: %s", rec.Body)
	}
}

// The authorization model chosen in docs/user-domain-design.md: writes are
// restricted to the caller's own account. If that decision is reversed, this is
// the test that should change with it.
func TestWritesAreRestrictedToTheCaller(t *testing.T) {
	svc := &fakeService{}

	for _, tc := range []struct{ method, body string }{
		{http.MethodPatch, `{"name":"Hijacked"}`},
		{http.MethodDelete, ""},
	} {
		t.Run(tc.method, func(t *testing.T) {
			rec := do(newAPI(svc, true), tc.method, "/api/v1/users/"+otherID, tc.body)

			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d when acting on another account", rec.Code, http.StatusForbidden)
			}
		})
	}
}

// Reads stay open to any authenticated caller under the same decision.
func TestReadsAreNotRestrictedToTheCaller(t *testing.T) {
	if rec := do(newAPI(&fakeService{}, true), http.MethodGet, "/api/v1/users/"+otherID, ""); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d reading another account", rec.Code, http.StatusOK)
	}
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields"`
} {
	t.Helper()

	var body struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response is not JSON (%s): %v", rec.Body, err)
	}
	return body
}

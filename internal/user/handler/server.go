// Package handler is the REST driving adapter for the User domain.
//
// It maps HTTP to the service and back: decoding, validation reporting, and
// status codes live here, and nothing else in the codebase knows about them.
// The gRPC adapter in gapi/ does the same job for its own protocol, over the
// same service, with no branching inside the service to tell them apart.
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/router"
	"github.com/DoIttikorn/e-commerce/internal/user"
)

// Service is the slice of the user service this adapter needs. Declared here,
// at the consumer, so the handler tests can drive a fake.
type Service interface {
	Register(ctx context.Context, in user.NewUser) (user.User, error)
	Authenticate(ctx context.Context, email, password string) (user.Token, error)
	ByID(ctx context.Context, id string) (user.User, error)
	List(ctx context.Context, limit, offset int) ([]user.User, int, error)
	Update(ctx context.Context, id string, upd user.Update) (user.User, error)
	Delete(ctx context.Context, id string) error
}

// Server holds the dependencies the endpoints share.
type Server struct {
	svc Service
	log *slog.Logger
}

// New returns a Server. The logger is used only for failures the client is not
// told about, so that a 500 leaves a trace without leaking one.
func New(svc Service, log *slog.Logger) *Server {
	return &Server{svc: svc, log: log}
}

// Register mounts the routes. requireAuth is the middleware that rejects
// requests without a valid token; it is passed in rather than constructed here
// so this package does not decide how authentication works.
func (s *Server) Register(r router.Router, requireAuth func(http.Handler) http.Handler) {
	r.Group("/api/v1", func(v1 router.Router) {
		v1.Group("/auth", func(a router.Router) {
			a.Handle(http.MethodPost, "/register", s.register)
			a.Handle(http.MethodPost, "/login", s.login)
		})

		v1.Group("/users", func(u router.Router) {
			u.Use(requireAuth)
			u.Handle(http.MethodGet, "/", s.listUsers)
			u.Handle(http.MethodPost, "/", s.createUser)
			u.Handle(http.MethodGet, "/{id}", s.getUser)
			u.Handle(http.MethodPatch, "/{id}", s.updateUser)
			u.Handle(http.MethodDelete, "/{id}", s.deleteUser)
		})
	})
}

// requireSelf enforces the authorization model.
//
// DECISION POINT, recorded in docs/user-domain-design.md under "Open question".
// The brief's entity has no role field, so there is no administrator to grant
// wider rights to. Two readings were available:
//
//	A. any authenticated caller may act on any user
//	B. reads open to any authenticated caller, writes restricted to the caller
//
// B is implemented here, because it costs one comparison and turns an obvious
// hole into a stated boundary. To switch to A, delete the two calls to this
// method — nothing else depends on it.
func (s *Server) requireSelf(w http.ResponseWriter, r *http.Request) bool {
	subject, ok := auth.SubjectFrom(r.Context())
	if !ok || subject != r.PathValue("id") {
		writeJSON(w, http.StatusForbidden, errorBody{
			Error: "you may only modify your own account",
		})
		return false
	}
	return true
}

// Package gapi is the gRPC driving adapter for the User domain.
//
// It sits beside handler/ and calls the same user.Service, which is the point
// of the hexagon: one set of business rules, two protocols, and no branch
// inside the service that knows which one it is serving. The only thing this
// package owns that handler/ does not is its own error vocabulary.
package gapi

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	userv1 "github.com/DoIttikorn/e-commerce/api/user/v1"
	"github.com/DoIttikorn/e-commerce/internal/user"
)

// Service is the slice of the user service this adapter needs.
//
// It is narrower than the one handler/ declares, because the gRPC surface is
// deliberately smaller: no list, no update, no delete, no login. Declaring the
// interface at the consumer is what lets each adapter ask for only what it uses.
type Service interface {
	Register(ctx context.Context, in user.NewUser) (user.User, error)
	ByID(ctx context.Context, id string) (user.User, error)
}

// Server implements the generated UserServiceServer.
type Server struct {
	// Embedding the Unimplemented server is what keeps adding an RPC to the
	// proto from breaking the build here before it is implemented.
	userv1.UnimplementedUserServiceServer

	svc Service
}

// New returns a Server backed by svc.
func New(svc Service) *Server {
	return &Server{svc: svc}
}

// toProto maps the entity onto the wire type.
//
// Written out rather than shared with the REST DTO on purpose: the two formats
// are free to diverge, and the entity carries tags for neither.
func toProto(u user.User) *userv1.User {
	return &userv1.User{
		Id:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: timestamppb.New(u.CreatedAt),
	}
}

// mapError translates a domain error into a gRPC status.
//
// This is the same job handler/writeError does for HTTP, and the reason the
// service returns domain errors rather than protocol ones: neither adapter has
// to know the other exists.
func mapError(err error) error {
	var verr *user.ValidationError

	switch {
	case errors.As(err, &verr):
		return invalidArgument(verr)

	case errors.Is(err, user.ErrInvalidID):
		return status.Error(codes.InvalidArgument, "malformed user id")

	case errors.Is(err, user.ErrUserNotFound):
		return status.Error(codes.NotFound, "user not found")

	case errors.Is(err, user.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "email already registered")

	case errors.Is(err, user.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "invalid credentials")

	default:
		// Reported as nothing, for the same reason the HTTP adapter reports a
		// bare 500: an unexpected error tends to carry a query or a hostname.
		// The caller in main logs the real one.
		return status.Error(codes.Internal, "internal error")
	}
}

// invalidArgument attaches per-field detail, which is the gRPC counterpart of
// the "fields" object the REST contract promises.
func invalidArgument(verr *user.ValidationError) error {
	st := status.New(codes.InvalidArgument, verr.Error())

	violations := make([]*errdetails.BadRequest_FieldViolation, 0, len(verr.Fields))
	for field, description := range verr.Fields {
		violations = append(violations, &errdetails.BadRequest_FieldViolation{
			Field:       field,
			Description: description,
		})
	}

	withDetails, err := st.WithDetails(&errdetails.BadRequest{FieldViolations: violations})
	if err != nil {
		// Attaching details can fail on marshalling; the bare status is still
		// a correct answer, so it is better than turning this into a 500.
		return st.Err()
	}
	return withDetails.Err()
}

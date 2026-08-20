package gapi

import (
	"context"

	userv1 "github.com/DoIttikorn/e-commerce/api/user/v1"
	"github.com/DoIttikorn/e-commerce/internal/user"
)

// CreateUser provisions an account for a calling service.
//
// It runs the same Register path the REST adapter uses, so validation, email
// normalisation, hashing and the unique-index guarantee are identical — there
// is no second implementation to drift.
func (s *Server) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.CreateUserResponse, error) {
	created, err := s.svc.Register(ctx, user.NewUser{
		Name:     req.GetName(),
		Email:    req.GetEmail(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, mapError(err)
	}

	return &userv1.CreateUserResponse{User: toProto(created)}, nil
}

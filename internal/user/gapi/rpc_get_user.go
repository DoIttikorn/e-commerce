package gapi

import (
	"context"

	userv1 "github.com/DoIttikorn/e-commerce/api/user/v1"
)

// GetUser resolves an ID to a user.
//
// This is the RPC the other domains will actually call: Order, Seller and
// Product all need to turn a stored user ID into a name to display.
func (s *Server) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.GetUserResponse, error) {
	found, err := s.svc.ByID(ctx, req.GetId())
	if err != nil {
		return nil, mapError(err)
	}

	return &userv1.GetUserResponse{User: toProto(found)}, nil
}

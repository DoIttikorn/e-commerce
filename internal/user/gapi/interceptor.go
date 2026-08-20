package gapi

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/DoIttikorn/e-commerce/internal/auth"
)

// authorizationKey is the metadata key gRPC clients use. gRPC lowercases
// metadata keys on the wire, so this must be lowercase to match.
const authorizationKey = "authorization"

// Verifier is the slice of the token service the interceptor needs.
type Verifier interface {
	Verify(raw string) (subject string, err error)
}

// AuthInterceptor rejects calls that do not carry a valid bearer token.
//
// The credential arrives in request metadata rather than in an HTTP header,
// which is the one part of authentication that genuinely differs between the
// two adapters. Verification is the same auth.Tokens both use, and the subject
// lands under the same context key, so nothing downstream can tell which
// protocol it was reached through.
func AuthInterceptor(v Verifier) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		next grpc.UnaryHandler,
	) (any, error) {
		raw, ok := tokenFromMetadata(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "unauthenticated")
		}

		subject, err := v.Verify(raw)
		if err != nil {
			// The reason is not reported, for the same reason the HTTP adapter
			// answers every rejection with a bare 401.
			return nil, status.Error(codes.Unauthenticated, "unauthenticated")
		}

		return next(auth.WithSubject(ctx, subject), req)
	}
}

func tokenFromMetadata(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}

	values := md.Get(authorizationKey)
	if len(values) == 0 {
		return "", false
	}
	return auth.BearerToken(values[0])
}

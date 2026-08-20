package integration

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	userv1 "github.com/DoIttikorn/e-commerce/api/user/v1"
	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/user"
	"github.com/DoIttikorn/e-commerce/internal/user/gapi"
)

const grpcTestSecret = "an-integration-test-secret-over-32-chars"

// startGRPC builds the real stack — Mongo adapter, bcrypt, HS256, service,
// gRPC adapter — and serves it on a real port, so this exercises the wiring
// and the interceptor rather than the handler functions in isolation.
func startGRPC(t *testing.T) (userv1.UserServiceClient, user.Service, context.Context) {
	t.Helper()

	repo, ctx := newTestRepo(t)
	tokens := auth.NewTokens(grpcTestSecret, time.Hour)
	// Cost 4 keeps the suite quick; production uses auth.DefaultCost.
	svc := user.NewService(repo, auth.NewHasher(4), tokens)

	srv := grpc.NewServer(grpc.UnaryInterceptor(gapi.AuthInterceptor(tokens)))
	userv1.RegisterUserServiceServer(srv, gapi.New(svc))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return userv1.NewUserServiceClient(conn), svc, ctx
}

// authed returns a context carrying a token for subject, in metadata rather
// than a header — the one part of authentication that differs from REST.
func authed(t *testing.T, ctx context.Context, subject string) context.Context {
	t.Helper()

	raw, _, err := auth.NewTokens(grpcTestSecret, time.Hour).Issue(subject)
	if err != nil {
		t.Fatalf("issuing a test token: %v", err)
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+raw)
}

func codeOf(err error) codes.Code {
	st, _ := status.FromError(err)
	return st.Code()
}

func TestGRPCRequiresAToken(t *testing.T) {
	client, _, ctx := startGRPC(t)

	_, err := client.GetUser(ctx, &userv1.GetUserRequest{Id: "anything"})

	if got := codeOf(err); got != codes.Unauthenticated {
		t.Errorf("code = %v, want %v", got, codes.Unauthenticated)
	}
}

func TestGRPCRejectsAForeignToken(t *testing.T) {
	client, _, ctx := startGRPC(t)

	foreign, _, err := auth.NewTokens("a-completely-different-secret-value-32", time.Hour).Issue("u")
	if err != nil {
		t.Fatalf("issuing the foreign token: %v", err)
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+foreign)

	_, err = client.GetUser(ctx, &userv1.GetUserRequest{Id: "anything"})

	if got := codeOf(err); got != codes.Unauthenticated {
		t.Errorf("code = %v, want %v", got, codes.Unauthenticated)
	}
}

// The full path: create over gRPC, read it back over gRPC.
func TestGRPCCreateThenGet(t *testing.T) {
	client, _, ctx := startGRPC(t)
	authCtx := authed(t, ctx, "caller")

	created, err := client.CreateUser(authCtx, &userv1.CreateUserRequest{
		Name:     "Ittikorn",
		Email:    "GRPC@Example.COM",
		Password: "correct-horse-battery",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	// Normalisation happens in the service, so it applies to both adapters.
	if got := created.GetUser().GetEmail(); got != "grpc@example.com" {
		t.Errorf("email = %q, want it lowercased by the service", got)
	}
	if created.GetUser().GetId() == "" || created.GetUser().GetCreatedAt() == nil {
		t.Fatalf("created user = %v, want an id and a timestamp", created.GetUser())
	}

	got, err := client.GetUser(authCtx, &userv1.GetUserRequest{Id: created.GetUser().GetId()})
	if err != nil {
		t.Fatalf("GetUser() error = %v", err)
	}
	if got.GetUser().GetId() != created.GetUser().GetId() {
		t.Errorf("GetUser returned %q, want %q", got.GetUser().GetId(), created.GetUser().GetId())
	}
}

// The unique index reaches gRPC as AlreadyExists, the same guarantee the REST
// adapter reports as 409 — one rule, two vocabularies.
func TestGRPCDuplicateEmailIsAlreadyExists(t *testing.T) {
	client, _, ctx := startGRPC(t)
	authCtx := authed(t, ctx, "caller")

	req := &userv1.CreateUserRequest{Name: "A", Email: "dup@example.com", Password: "correct-horse-battery"}
	if _, err := client.CreateUser(authCtx, req); err != nil {
		t.Fatalf("first CreateUser() error = %v", err)
	}

	_, err := client.CreateUser(authCtx, req)

	if got := codeOf(err); got != codes.AlreadyExists {
		t.Errorf("code = %v, want %v", got, codes.AlreadyExists)
	}
}

func TestGRPCErrorCodes(t *testing.T) {
	client, _, ctx := startGRPC(t)
	authCtx := authed(t, ctx, "caller")

	tests := []struct {
		name string
		id   string
		want codes.Code
	}{
		{"malformed id", "not-an-object-id", codes.InvalidArgument},
		{"well-formed but absent", "000000000000000000000000", codes.NotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.GetUser(authCtx, &userv1.GetUserRequest{Id: tt.id})

			if got := codeOf(err); got != tt.want {
				t.Errorf("code = %v, want %v", got, tt.want)
			}
		})
	}
}

// A user created over gRPC must be visible to the REST side, because there is
// one service and one collection behind both adapters.
func TestGRPCAndRESTShareOneStore(t *testing.T) {
	client, svc, ctx := startGRPC(t)
	authCtx := authed(t, ctx, "caller")

	created, err := client.CreateUser(authCtx, &userv1.CreateUserRequest{
		Name: "Shared", Email: "shared@example.com", Password: "correct-horse-battery",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	// Same service instance the HTTP handler would be holding.
	found, err := svc.ByID(ctx, created.GetUser().GetId())
	if err != nil {
		t.Fatalf("service.ByID() error = %v", err)
	}
	if found.Email != "shared@example.com" {
		t.Errorf("email = %q, want the user created over gRPC", found.Email)
	}
	if found.PasswordHash != "" {
		t.Error("the service returned a password hash")
	}
}

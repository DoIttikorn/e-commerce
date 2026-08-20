package gapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	userv1 "github.com/DoIttikorn/e-commerce/api/user/v1"
	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/user"
	"github.com/DoIttikorn/e-commerce/internal/user/gapi"
)

type fakeService struct {
	registerFn func(context.Context, user.NewUser) (user.User, error)
	byIDFn     func(context.Context, string) (user.User, error)

	gotNewUser user.NewUser
}

func (f *fakeService) Register(ctx context.Context, in user.NewUser) (user.User, error) {
	f.gotNewUser = in
	if f.registerFn != nil {
		return f.registerFn(ctx, in)
	}
	return user.User{ID: "new-id", Name: in.Name, Email: in.Email, CreatedAt: time.Now()}, nil
}

func (f *fakeService) ByID(ctx context.Context, id string) (user.User, error) {
	if f.byIDFn != nil {
		return f.byIDFn(ctx, id)
	}
	return user.User{ID: id, Name: "N", Email: "n@example.com", CreatedAt: time.Now()}, nil
}

func TestCreateUserPassesInputThroughAndReturnsTheUser(t *testing.T) {
	svc := &fakeService{}

	got, err := gapi.New(svc).CreateUser(context.Background(), &userv1.CreateUserRequest{
		Name:     "Ittikorn",
		Email:    "i@example.com",
		Password: "correct-horse-battery",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	if svc.gotNewUser.Email != "i@example.com" || svc.gotNewUser.Password != "correct-horse-battery" {
		t.Errorf("service got %+v, want the request values", svc.gotNewUser)
	}
	if got.GetUser().GetId() == "" || got.GetUser().GetCreatedAt() == nil {
		t.Errorf("response = %v, want a populated user", got)
	}
}

func TestGetUserReturnsTheUser(t *testing.T) {
	got, err := gapi.New(&fakeService{}).GetUser(context.Background(), &userv1.GetUserRequest{Id: "abc"})
	if err != nil {
		t.Fatalf("GetUser() error = %v", err)
	}

	if got.GetUser().GetId() != "abc" {
		t.Errorf("id = %q, want %q", got.GetUser().GetId(), "abc")
	}
}

// The same domain errors the REST adapter turns into status codes must become
// the gRPC equivalents here, with neither adapter knowing about the other.
func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		svcErr   error
		wantCode codes.Code
	}{
		{"not found", user.ErrUserNotFound, codes.NotFound},
		{"duplicate email", user.ErrEmailTaken, codes.AlreadyExists},
		{"malformed id", user.ErrInvalidID, codes.InvalidArgument},
		{"bad credentials", user.ErrInvalidCredentials, codes.Unauthenticated},
		{"validation", &user.ValidationError{Fields: map[string]string{"email": "must be a valid email address"}}, codes.InvalidArgument},
		{"unexpected", errors.New("mongo exploded"), codes.Internal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{byIDFn: func(context.Context, string) (user.User, error) {
				return user.User{}, tt.svcErr
			}}

			_, err := gapi.New(svc).GetUser(context.Background(), &userv1.GetUserRequest{Id: "x"})

			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("error = %v, want a gRPC status", err)
			}
			if st.Code() != tt.wantCode {
				t.Errorf("code = %v, want %v", st.Code(), tt.wantCode)
			}
			// An internal failure must not describe itself to the caller.
			if strings.Contains(st.Message(), "mongo exploded") {
				t.Errorf("internal error leaked: %q", st.Message())
			}
		})
	}
}

// The gRPC counterpart of the REST "fields" object.
func TestValidationErrorCarriesFieldViolations(t *testing.T) {
	svc := &fakeService{registerFn: func(context.Context, user.NewUser) (user.User, error) {
		return user.User{}, &user.ValidationError{Fields: map[string]string{
			"email": "must be a valid email address",
			"name":  "is required",
		}}
	}}

	_, err := gapi.New(svc).CreateUser(context.Background(), &userv1.CreateUserRequest{})

	st, _ := status.FromError(err)
	var got []string
	for _, detail := range st.Details() {
		br, ok := detail.(*errdetails.BadRequest)
		if !ok {
			continue
		}
		for _, v := range br.GetFieldViolations() {
			got = append(got, v.GetField())
		}
	}

	if len(got) != 2 {
		t.Fatalf("field violations = %v, want two entries", got)
	}
}

// Structural, like the REST DTO: the hash cannot be serialised because the
// message has nowhere to put it.
func TestProtoUserHasNoPasswordField(t *testing.T) {
	fields := (&userv1.User{}).ProtoReflect().Descriptor().Fields()

	for i := range fields.Len() {
		if name := string(fields.Get(i).Name()); strings.Contains(name, "password") {
			t.Errorf("the User message has a %q field; it must never carry credentials", name)
		}
	}
}

type fakeVerifier struct {
	subject string
	err     error
}

func (f fakeVerifier) Verify(string) (string, error) { return f.subject, f.err }

func TestAuthInterceptorRejectsMissingAndBadTokens(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		verifier fakeVerifier
	}{
		{"no metadata at all", context.Background(), fakeVerifier{subject: "u"}},
		{"metadata without authorization", withMD(), fakeVerifier{subject: "u"}},
		{"wrong scheme", withMD("authorization", "Basic dXNlcjpwYXNz"), fakeVerifier{subject: "u"}},
		{"empty bearer", withMD("authorization", "Bearer "), fakeVerifier{subject: "u"}},
		{"token rejected", withMD("authorization", "Bearer bad"), fakeVerifier{err: errors.New("nope")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := func(context.Context, any) (any, error) { called = true; return nil, nil }

			_, err := gapi.AuthInterceptor(tt.verifier)(tt.ctx, nil, &grpc.UnaryServerInfo{}, next)

			if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
				t.Errorf("code = %v, want %v", st.Code(), codes.Unauthenticated)
			}
			if called {
				t.Error("the RPC ran despite the call being rejected")
			}
		})
	}
}

// The subject must land under the same context key the HTTP middleware uses,
// so code below the adapters cannot tell the protocols apart.
func TestAuthInterceptorPassesSubjectThrough(t *testing.T) {
	var gotSubject string
	next := func(ctx context.Context, _ any) (any, error) {
		gotSubject, _ = auth.SubjectFrom(ctx)
		return nil, nil
	}

	_, err := gapi.AuthInterceptor(fakeVerifier{subject: "user-9"})(
		withMD("authorization", "Bearer good"), nil, &grpc.UnaryServerInfo{}, next)
	if err != nil {
		t.Fatalf("interceptor error = %v", err)
	}

	if gotSubject != "user-9" {
		t.Errorf("subject = %q, want %q", gotSubject, "user-9")
	}
}

func withMD(pairs ...string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(pairs...))
}

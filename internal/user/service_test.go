package user

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The fakes below are hand-written and live with the test, which is the whole
// return on declaring Repository, Hasher and TokenIssuer as ports: this file
// exercises every business rule with no database, no HTTP, and no bcrypt.

type fakeRepo struct {
	createFn  func(context.Context, User) (User, error)
	byIDFn    func(context.Context, string) (User, error)
	byEmailFn func(context.Context, string) (User, error)
	listFn    func(context.Context, int, int) ([]User, int, error)
	updateFn  func(context.Context, string, Update) (User, error)
	deleteFn  func(context.Context, string) error
	countFn   func(context.Context) (int64, error)

	gotUser   User
	gotUpdate Update
	gotLimit  int
	gotOffset int
}

func (f *fakeRepo) Create(ctx context.Context, u User) (User, error) {
	f.gotUser = u
	if f.createFn != nil {
		return f.createFn(ctx, u)
	}
	u.ID = "generated-id"
	return u, nil
}

func (f *fakeRepo) ByID(ctx context.Context, id string) (User, error) {
	if f.byIDFn != nil {
		return f.byIDFn(ctx, id)
	}
	return User{ID: id, PasswordHash: "leaky"}, nil
}

func (f *fakeRepo) ByEmail(ctx context.Context, email string) (User, error) {
	if f.byEmailFn != nil {
		return f.byEmailFn(ctx, email)
	}
	return User{}, ErrUserNotFound
}

func (f *fakeRepo) List(ctx context.Context, limit, offset int) ([]User, int, error) {
	f.gotLimit, f.gotOffset = limit, offset
	if f.listFn != nil {
		return f.listFn(ctx, limit, offset)
	}
	return []User{{ID: "1", PasswordHash: "leaky"}}, 1, nil
}

func (f *fakeRepo) Update(ctx context.Context, id string, upd Update) (User, error) {
	f.gotUpdate = upd
	if f.updateFn != nil {
		return f.updateFn(ctx, id, upd)
	}
	return User{ID: id, PasswordHash: "leaky"}, nil
}

func (f *fakeRepo) Delete(ctx context.Context, id string) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id)
	}
	return nil
}

func (f *fakeRepo) Count(ctx context.Context) (int64, error) {
	if f.countFn != nil {
		return f.countFn(ctx)
	}
	return 7, nil
}

// fakeHasher is a reversible stand-in, so tests state expected hashes directly.
type fakeHasher struct{ compares int }

func (h *fakeHasher) Hash(plain string) (string, error) { return "hashed:" + plain, nil }

func (h *fakeHasher) Compare(hash, plain string) error {
	h.compares++
	if hash != "hashed:"+plain {
		return errors.New("mismatch")
	}
	return nil
}

type fakeTokens struct{ gotSubject string }

func (t *fakeTokens) Issue(subject string) (string, time.Time, error) {
	t.gotSubject = subject
	return "token-for-" + subject, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), nil
}

func newTestService(repo *fakeRepo) (Service, *fakeHasher, *fakeTokens) {
	hasher, tokens := &fakeHasher{}, &fakeTokens{}
	svc := NewService(repo, hasher, tokens)
	hasher.compares = 0 // NewService pre-hashes the decoy; ignore that here.
	return svc, hasher, tokens
}

const validPassword = "correct-horse-battery"

func TestRegisterStoresNormalizedEmailAndHash(t *testing.T) {
	repo := &fakeRepo{}
	svc, _, _ := newTestService(repo)

	got, err := svc.Register(context.Background(), NewUser{
		Name:     "  Ittikorn  ",
		Email:    "  Ittikorn@Example.COM  ",
		Password: validPassword,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Lowercased on write, or the unique index cannot do its job.
	if repo.gotUser.Email != "ittikorn@example.com" {
		t.Errorf("stored email = %q, want %q", repo.gotUser.Email, "ittikorn@example.com")
	}
	if repo.gotUser.Name != "Ittikorn" {
		t.Errorf("stored name = %q, want it trimmed", repo.gotUser.Name)
	}
	if repo.gotUser.PasswordHash != "hashed:"+validPassword {
		t.Errorf("stored hash = %q, want the hashed password", repo.gotUser.PasswordHash)
	}
	if repo.gotUser.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set")
	}
	if got.PasswordHash != "" {
		t.Error("Register returned a password hash; it must never leave the domain")
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		in        NewUser
		wantField string
	}{
		{"blank name", NewUser{Name: "   ", Email: "a@b.com", Password: validPassword}, "name"},
		{"malformed email", NewUser{Name: "A", Email: "not-an-email", Password: validPassword}, "email"},
		{"email with display name", NewUser{Name: "A", Email: "Bob <b@c.com>", Password: validPassword}, "email"},
		{"missing password", NewUser{Name: "A", Email: "a@b.com"}, "password"},
		{"short password", NewUser{Name: "A", Email: "a@b.com", Password: "short"}, "password"},
		// bcrypt refuses input over 72 bytes; caught here it is a 400, not a 500.
		{"password over 72 bytes", NewUser{Name: "A", Email: "a@b.com", Password: strings.Repeat("x", 73)}, "password"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := newTestService(&fakeRepo{})

			_, err := svc.Register(context.Background(), tt.in)

			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("error = %v, want a *ValidationError", err)
			}
			if _, ok := verr.Fields[tt.wantField]; !ok {
				t.Errorf("fields = %v, want an entry for %q", verr.Fields, tt.wantField)
			}
		})
	}
}

func TestRegisterPropagatesEmailTaken(t *testing.T) {
	repo := &fakeRepo{createFn: func(context.Context, User) (User, error) {
		return User{}, ErrEmailTaken
	}}
	svc, _, _ := newTestService(repo)

	_, err := svc.Register(context.Background(), NewUser{
		Name: "A", Email: "a@b.com", Password: validPassword,
	})

	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("error = %v, want ErrEmailTaken", err)
	}
}

func TestAuthenticateIssuesTokenForCorrectPassword(t *testing.T) {
	repo := &fakeRepo{byEmailFn: func(_ context.Context, email string) (User, error) {
		return User{ID: "user-1", Email: email, PasswordHash: "hashed:" + validPassword}, nil
	}}
	svc, _, tokens := newTestService(repo)

	got, err := svc.Authenticate(context.Background(), "A@B.com", validPassword)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	if tokens.gotSubject != "user-1" {
		t.Errorf("token subject = %q, want the user ID", tokens.gotSubject)
	}
	if got.Value != "token-for-user-1" {
		t.Errorf("token = %q, want the issued value", got.Value)
	}
	if got.ExpiresAt.IsZero() {
		t.Error("ExpiresAt was not set")
	}
}

// Every failure must be the same error, or the response tells an attacker which
// addresses are registered.
func TestAuthenticateFailuresAreIndistinguishable(t *testing.T) {
	tests := []struct {
		name     string
		repo     *fakeRepo
		email    string
		password string
	}{
		{
			name:     "unknown email",
			repo:     &fakeRepo{byEmailFn: func(context.Context, string) (User, error) { return User{}, ErrUserNotFound }},
			email:    "nobody@example.com",
			password: validPassword,
		},
		{
			name: "wrong password",
			repo: &fakeRepo{byEmailFn: func(_ context.Context, e string) (User, error) {
				return User{ID: "user-1", Email: e, PasswordHash: "hashed:" + validPassword}, nil
			}},
			email:    "a@b.com",
			password: "the-wrong-password",
		},
		{
			name:     "malformed email",
			repo:     &fakeRepo{},
			email:    "not-an-email",
			password: validPassword,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, hasher, _ := newTestService(tt.repo)

			_, err := svc.Authenticate(context.Background(), tt.email, tt.password)

			if !errors.Is(err, ErrInvalidCredentials) {
				t.Errorf("error = %v, want ErrInvalidCredentials", err)
			}
			// Same error is not enough: without a comparison on the miss
			// paths, the faster response still gives the answer away.
			if hasher.compares != 1 {
				t.Errorf("Compare called %d times, want exactly 1 so every path costs the same", hasher.compares)
			}
		})
	}
}

func TestUpdateRequiresAtLeastOneField(t *testing.T) {
	svc, _, _ := newTestService(&fakeRepo{})

	_, err := svc.Update(context.Background(), "id", Update{})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want a *ValidationError", err)
	}
}

// PATCH semantics: an omitted field must reach the repository as nil, so the
// stored value is left alone rather than blanked.
func TestUpdatePassesOnlySuppliedFields(t *testing.T) {
	repo := &fakeRepo{}
	svc, _, _ := newTestService(repo)
	name := "  New Name  "

	if _, err := svc.Update(context.Background(), "id", Update{Name: &name}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if repo.gotUpdate.Email != nil {
		t.Error("Email reached the repository as non-nil; an omitted field must stay nil")
	}
	if repo.gotUpdate.Name == nil || *repo.gotUpdate.Name != "New Name" {
		t.Errorf("Name = %v, want the trimmed value", repo.gotUpdate.Name)
	}
}

func TestUpdateNormalizesAndValidatesEmail(t *testing.T) {
	repo := &fakeRepo{}
	svc, _, _ := newTestService(repo)
	good := " Mixed@Case.COM "

	if _, err := svc.Update(context.Background(), "id", Update{Email: &good}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if repo.gotUpdate.Email == nil || *repo.gotUpdate.Email != "mixed@case.com" {
		t.Errorf("Email = %v, want it normalized", repo.gotUpdate.Email)
	}

	bad := "nope"
	if _, err := svc.Update(context.Background(), "id", Update{Email: &bad}); err == nil {
		t.Error("Update() accepted a malformed email")
	}
}

func TestListClampsPaging(t *testing.T) {
	tests := []struct {
		name                  string
		limit, offset         int
		wantLimit, wantOffset int
	}{
		{"defaults applied", 0, 0, DefaultPageSize, 0},
		{"negative limit", -5, -5, DefaultPageSize, 0},
		{"over maximum", MaxPageSize + 1000, 10, MaxPageSize, 10},
		{"within bounds", 5, 3, 5, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc, _, _ := newTestService(repo)

			if _, _, err := svc.List(context.Background(), tt.limit, tt.offset); err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if repo.gotLimit != tt.wantLimit || repo.gotOffset != tt.wantOffset {
				t.Errorf("repo got limit=%d offset=%d, want limit=%d offset=%d",
					repo.gotLimit, repo.gotOffset, tt.wantLimit, tt.wantOffset)
			}
		})
	}
}

// The hash must be stripped on every path out of the domain, not just Register.
func TestReadsNeverReturnPasswordHash(t *testing.T) {
	svc, _, _ := newTestService(&fakeRepo{})

	got, err := svc.ByID(context.Background(), "id")
	if err != nil {
		t.Fatalf("ByID() error = %v", err)
	}
	if got.PasswordHash != "" {
		t.Error("ByID returned a password hash")
	}

	users, _, err := svc.List(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(users) == 0 || users[0].PasswordHash != "" {
		t.Error("List returned a password hash")
	}
}

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"a@b.com", "a@b.com", true},
		{"  A@B.COM  ", "a@b.com", true},
		{"not-an-email", "", false},
		{"Bob <bob@x.com>", "", false},
		{"", "", false},
		{"a@b@c.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := normalizeEmail(tt.in)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("normalizeEmail(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

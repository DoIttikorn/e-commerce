package user

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// Page size bounds for List. The brief asks to "list all users"; an unbounded
// list endpoint is a denial of service waiting for the collection to grow, so
// the service caps it and the adapter documents the default.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Password length bounds. The maximum is not arbitrary: bcrypt refuses input
// longer than 72 bytes, and rejecting it here turns a 500 into a 400.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 72
)

// Hasher hides the password hashing algorithm from the domain. Declared here,
// implemented in internal/auth, so this package never imports bcrypt.
type Hasher interface {
	Hash(plain string) (string, error)
	// Compare returns a non-nil error when plain does not match hash.
	Compare(hash, plain string) error
}

// TokenIssuer hides the token format from the domain, for the same reason.
type TokenIssuer interface {
	Issue(subject string) (token string, expiresAt time.Time, err error)
}

// Service is everything the User domain can do.
//
// The driving adapters each declare a narrower interface covering only what
// they use — handler/ needs six methods, gapi/ needs two — which is the right
// direction for a dependency. This one exists for the two things those cannot
// give you: a single place that answers "what does this domain do?", and a
// single type to fake when a test needs the whole service rather than a slice
// of it.
type Service interface {
	// Register creates an account. Returns ErrEmailTaken if the address is
	// held, from the unique index rather than a preceding read.
	Register(ctx context.Context, in NewUser) (User, error)

	// Authenticate exchanges credentials for a token. Every failure is
	// ErrInvalidCredentials, and costs the same time, so neither the response
	// nor its timing distinguishes an unknown address from a wrong password.
	Authenticate(ctx context.Context, email, password string) (Token, error)

	// ByID returns one user, or ErrUserNotFound / ErrInvalidID.
	ByID(ctx context.Context, id string) (User, error)

	// List returns one page and the total count, with paging clamped.
	List(ctx context.Context, limit, offset int) (users []User, total int, err error)

	// Update applies only the fields upd carries; nil means leave alone.
	Update(ctx context.Context, id string, upd Update) (User, error)

	Delete(ctx context.Context, id string) error

	// Count backs the periodic user-count log.
	Count(ctx context.Context) (int64, error)
}

// service is the implementation. It knows nothing about HTTP, gRPC, or
// MongoDB: everything it needs arrives through the three ports above.
type service struct {
	repo   Repository
	hasher Hasher
	tokens TokenIssuer

	// decoyHash is compared against when an email is unknown, so that the
	// unknown-email path costs the same as a real comparison. Without it the
	// response time reveals whether an address is registered, which would
	// undo the single shared ErrInvalidCredentials. Computed once here rather
	// than per request, because a bcrypt hash is deliberately expensive.
	decoyHash string
}

// NewUser is the input to Register.
type NewUser struct {
	Name     string
	Email    string
	Password string
}

// Token is an issued credential and the moment it stops being valid.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

var _ Service = (*service)(nil)

// NewService wires the domain to its adapters.
func NewService(repo Repository, hasher Hasher, tokens TokenIssuer) Service {
	s := &service{repo: repo, hasher: hasher, tokens: tokens}

	// A failure here would leave the decoy empty, which only costs the timing
	// defence — never a working login — so it is not worth failing startup.
	s.decoyHash, _ = hasher.Hash("a value that is never anybody's password")
	return s
}

// Register creates a user.
//
// There is deliberately no "does this email exist?" query first. Two concurrent
// registrations would both pass such a check and both proceed; the unique index
// is the only guard that actually holds, so the code relies on it and the
// adapter translates its error.
func (s *service) Register(ctx context.Context, in NewUser) (User, error) {
	name := strings.TrimSpace(in.Name)
	fields := map[string]string{}

	if name == "" {
		fields["name"] = "is required"
	}

	email, ok := normalizeEmail(in.Email)
	if !ok {
		fields["email"] = "must be a valid email address"
	}
	if msg := checkPassword(in.Password); msg != "" {
		fields["password"] = msg
	}
	if err := newValidationError(fields); err != nil {
		return User{}, err
	}

	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}

	created, err := s.repo.Create(ctx, User{
		Name:         name,
		Email:        email,
		PasswordHash: hash,
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return User{}, err
	}
	return withoutHash(created), nil
}

// Authenticate exchanges credentials for a token.
//
// Every failure returns ErrInvalidCredentials, and the unknown-email path still
// performs a comparison, so neither the response nor its timing distinguishes
// an unregistered address from a wrong password.
func (s *service) Authenticate(ctx context.Context, email, password string) (Token, error) {
	normalized, ok := normalizeEmail(email)
	if !ok {
		// Not a validation error: a malformed address on login is answered
		// exactly like a wrong one.
		s.burnComparison(password)
		return Token{}, ErrInvalidCredentials
	}

	found, err := s.repo.ByEmail(ctx, normalized)
	switch {
	case errors.Is(err, ErrUserNotFound):
		s.burnComparison(password)
		return Token{}, ErrInvalidCredentials
	case err != nil:
		return Token{}, err
	}

	if err := s.hasher.Compare(found.PasswordHash, password); err != nil {
		return Token{}, ErrInvalidCredentials
	}

	value, expiresAt, err := s.tokens.Issue(found.ID)
	if err != nil {
		return Token{}, fmt.Errorf("issue token: %w", err)
	}
	return Token{Value: value, ExpiresAt: expiresAt}, nil
}

// burnComparison spends the same time a real password check would.
func (s *service) burnComparison(password string) {
	_ = s.hasher.Compare(s.decoyHash, password)
}

// ByID returns one user.
func (s *service) ByID(ctx context.Context, id string) (User, error) {
	found, err := s.repo.ByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	return withoutHash(found), nil
}

// List returns one page of users and the total count.
func (s *service) List(ctx context.Context, limit, offset int) ([]User, int, error) {
	limit, offset = ClampPage(limit, offset)

	users, total, err := s.repo.List(ctx, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	for i := range users {
		users[i] = withoutHash(users[i])
	}
	return users, total, nil
}

// Update changes a name, an email, or both. Fields left nil are untouched.
func (s *service) Update(ctx context.Context, id string, upd Update) (User, error) {
	if upd.IsEmpty() {
		return User{}, &ValidationError{Fields: map[string]string{
			"body": "supply at least one of name or email",
		}}
	}

	fields := map[string]string{}

	if upd.Name != nil {
		name := strings.TrimSpace(*upd.Name)
		if name == "" {
			fields["name"] = "must not be blank"
		}
		upd.Name = &name
	}
	if upd.Email != nil {
		email, ok := normalizeEmail(*upd.Email)
		if !ok {
			fields["email"] = "must be a valid email address"
		}
		upd.Email = &email
	}
	if err := newValidationError(fields); err != nil {
		return User{}, err
	}

	updated, err := s.repo.Update(ctx, id, upd)
	if err != nil {
		return User{}, err
	}
	return withoutHash(updated), nil
}

// Delete removes a user.
func (s *service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// Count returns the total number of users, for the periodic count log.
func (s *service) Count(ctx context.Context) (int64, error) {
	return s.repo.Count(ctx)
}

// withoutHash blanks the password hash on its way out of the domain.
//
// Adapters already map into their own structs, so the hash cannot reach a
// response through the normal path. This is the second guard, for the day
// somebody adds an adapter that marshals the entity directly.
func withoutHash(u User) User {
	u.PasswordHash = ""
	return u
}

// normalizeEmail trims, validates, and lowercases an address.
//
// Lowercasing on write rather than on read is what makes the unique index
// effective: normalising only on read would leave A@b.com and a@b.com both
// stored, and by then it is too late.
func normalizeEmail(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)

	addr, err := mail.ParseAddress(trimmed)
	// A display name means the input was "Bob <bob@x.com>" rather than a bare
	// address, which is not what an email field should accept.
	if err != nil || addr.Name != "" || addr.Address != trimmed {
		return "", false
	}
	return strings.ToLower(addr.Address), true
}

func checkPassword(p string) string {
	switch {
	case p == "":
		return "is required"
	case len(p) < MinPasswordLen:
		return fmt.Sprintf("must be at least %d characters", MinPasswordLen)
	case len(p) > MaxPasswordLen:
		return fmt.Sprintf("must be at most %d bytes", MaxPasswordLen)
	default:
		return ""
	}
}

// ClampPage applies the paging bounds. Exported so an adapter can report the
// paging that was actually used rather than what the caller asked for.
func ClampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

package user

import "context"

// Repository is the persistence port.
//
// It is declared here, by the consumer, and implemented by an adapter — never
// the other way round. That direction is what lets the service be tested with a
// fake and what keeps the driver out of this package.
type Repository interface {
	// Create stores a new user. It returns ErrEmailTaken if the email is
	// already held, which the adapter derives from the unique index rather
	// than from a preceding read.
	Create(ctx context.Context, u User) (User, error)

	// ByID returns ErrUserNotFound if nothing matches, and ErrInvalidID if id
	// is not a well-formed identifier — two different failures that deserve
	// two different responses.
	ByID(ctx context.Context, id string) (User, error)

	// ByEmail returns ErrUserNotFound if nothing matches.
	ByEmail(ctx context.Context, email string) (User, error)

	// List returns one page and the total number of users.
	List(ctx context.Context, limit, offset int) (users []User, total int, err error)

	// Update applies only the fields upd carries.
	Update(ctx context.Context, id string, upd Update) (User, error)

	Delete(ctx context.Context, id string) error

	// Count backs the periodic user-count log.
	Count(ctx context.Context) (int64, error)
}

// Update carries only the fields the caller supplied. A nil field means "leave
// this alone", which a struct of plain strings could not express: it cannot
// distinguish an omitted name from a name set to the empty string.
type Update struct {
	Name  *string
	Email *string
}

// IsEmpty reports whether the update would change nothing.
func (u Update) IsEmpty() bool { return u.Name == nil && u.Email == nil }

// Package integration holds tests that need real infrastructure.
//
// They are skipped under -short, which is what `make test` passes, and run by
// `make itest` against a MongoDB instance.
package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/DoIttikorn/e-commerce/internal/user"
	usermongo "github.com/DoIttikorn/e-commerce/internal/user/mongodb"
)

func newTestRepo(t *testing.T) (*usermongo.Repository, context.Context) {
	t.Helper()

	if testing.Short() {
		t.Skip("needs MongoDB; run make itest")
	}

	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		uri = "mongodb://127.0.0.1:27017"
	}
	dbName := os.Getenv("MONGO_DATABASE")
	if dbName == "" {
		dbName = "ecommerce_test"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("mongo unreachable at %s: %v", uri, err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	db := client.Database(dbName)

	// Drop rather than delete, so each test also re-creates the indexes and
	// therefore exercises EnsureIndexes rather than trusting it ran once.
	if err := db.Collection(usermongo.CollectionName).Drop(ctx); err != nil {
		t.Fatalf("drop collection: %v", err)
	}

	repo := usermongo.NewRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	return repo, ctx
}

func sample(email string) user.User {
	return user.User{
		Name:         "Test User",
		Email:        email,
		PasswordHash: "not-a-real-hash",
		CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestCreateAndReadBack(t *testing.T) {
	repo, ctx := newTestRepo(t)

	created, err := repo.Create(ctx, sample("a@example.com"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create() returned an empty ID")
	}

	byID, err := repo.ByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("ByID() error = %v", err)
	}
	if byID.Email != "a@example.com" || byID.PasswordHash != "not-a-real-hash" {
		t.Errorf("ByID() = %+v, want the stored user", byID)
	}

	byEmail, err := repo.ByEmail(ctx, "a@example.com")
	if err != nil {
		t.Fatalf("ByEmail() error = %v", err)
	}
	if byEmail.ID != created.ID {
		t.Errorf("ByEmail() ID = %q, want %q", byEmail.ID, created.ID)
	}
}

// The unique index is the point of this whole file: no fake can prove it, and
// without it email uniqueness is a hope rather than a guarantee.
func TestDuplicateEmailIsRejected(t *testing.T) {
	repo, ctx := newTestRepo(t)

	if _, err := repo.Create(ctx, sample("dup@example.com")); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	_, err := repo.Create(ctx, sample("dup@example.com"))

	if !errors.Is(err, user.ErrEmailTaken) {
		t.Fatalf("second Create() error = %v, want ErrEmailTaken", err)
	}
}

// The reason the code inserts without checking first: a read-then-write guard
// is passed by every one of these goroutines before any of them writes.
func TestUniqueIndexHoldsUnderConcurrentInsert(t *testing.T) {
	repo, ctx := newTestRepo(t)

	const attempts = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, attempts)

	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // line them up so the inserts genuinely overlap
			_, err := repo.Create(ctx, sample("race@example.com"))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var created, rejected int
	for err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, user.ErrEmailTaken):
			rejected++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if created != 1 {
		t.Errorf("%d inserts succeeded, want exactly 1", created)
	}
	if rejected != attempts-1 {
		t.Errorf("%d inserts rejected, want %d", rejected, attempts-1)
	}
}

func TestByIDDistinguishesMalformedFromMissing(t *testing.T) {
	repo, ctx := newTestRepo(t)

	if _, err := repo.ByID(ctx, "not-an-object-id"); !errors.Is(err, user.ErrInvalidID) {
		t.Errorf("malformed ID error = %v, want ErrInvalidID", err)
	}
	// Well-formed, but nothing has this ID.
	if _, err := repo.ByID(ctx, "000000000000000000000000"); !errors.Is(err, user.ErrUserNotFound) {
		t.Errorf("missing ID error = %v, want ErrUserNotFound", err)
	}
}

func TestUpdateAppliesOnlySuppliedFields(t *testing.T) {
	repo, ctx := newTestRepo(t)
	created, err := repo.Create(ctx, sample("before@example.com"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	newName := "Renamed"
	updated, err := repo.Update(ctx, created.ID, user.Update{Name: &newName})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if updated.Name != "Renamed" {
		t.Errorf("Name = %q, want %q", updated.Name, "Renamed")
	}
	if updated.Email != "before@example.com" {
		t.Errorf("Email = %q, want it untouched by a name-only update", updated.Email)
	}
	if updated.CreatedAt.IsZero() {
		t.Error("CreatedAt was lost by the update")
	}
}

// Changing to an address another user holds must fail the same way a duplicate
// registration does.
func TestUpdateToTakenEmailIsRejected(t *testing.T) {
	repo, ctx := newTestRepo(t)

	if _, err := repo.Create(ctx, sample("first@example.com")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	second, err := repo.Create(ctx, sample("second@example.com"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	taken := "first@example.com"
	_, err = repo.Update(ctx, second.ID, user.Update{Email: &taken})

	if !errors.Is(err, user.ErrEmailTaken) {
		t.Errorf("Update() error = %v, want ErrEmailTaken", err)
	}
}

func TestUpdateAndDeleteReportMissingUser(t *testing.T) {
	repo, ctx := newTestRepo(t)
	const absent = "000000000000000000000000"
	name := "Nobody"

	if _, err := repo.Update(ctx, absent, user.Update{Name: &name}); !errors.Is(err, user.ErrUserNotFound) {
		t.Errorf("Update() error = %v, want ErrUserNotFound", err)
	}
	if err := repo.Delete(ctx, absent); !errors.Is(err, user.ErrUserNotFound) {
		t.Errorf("Delete() error = %v, want ErrUserNotFound", err)
	}
}

func TestDeleteRemovesTheUser(t *testing.T) {
	repo, ctx := newTestRepo(t)
	created, err := repo.Create(ctx, sample("gone@example.com"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := repo.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repo.ByID(ctx, created.ID); !errors.Is(err, user.ErrUserNotFound) {
		t.Errorf("ByID() after delete error = %v, want ErrUserNotFound", err)
	}
	// The address must be free again, or delete has left a tombstone.
	if _, err := repo.Create(ctx, sample("gone@example.com")); err != nil {
		t.Errorf("re-creating the deleted email error = %v, want success", err)
	}
}

func TestListPaginatesAndTotals(t *testing.T) {
	repo, ctx := newTestRepo(t)

	const total = 5
	for i := range total {
		u := sample(fmt.Sprintf("user%d@example.com", i))
		// Distinct timestamps so the ordering under test is deterministic.
		u.CreatedAt = u.CreatedAt.Add(time.Duration(i) * time.Second)
		if _, err := repo.Create(ctx, u); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	first, gotTotal, err := repo.List(ctx, 2, 0)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if gotTotal != total {
		t.Errorf("total = %d, want %d", gotTotal, total)
	}
	if len(first) != 2 {
		t.Fatalf("page size = %d, want 2", len(first))
	}

	second, _, err := repo.List(ctx, 2, 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second page size = %d, want 2", len(second))
	}

	// Pages must not overlap, which is what the _id tiebreaker in the sort is for.
	for _, a := range first {
		for _, b := range second {
			if a.ID == b.ID {
				t.Errorf("user %s appears on both pages", a.ID)
			}
		}
	}

	last, _, err := repo.List(ctx, 2, 4)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(last) != 1 {
		t.Errorf("final page size = %d, want 1", len(last))
	}
}

func TestCountReflectsInserts(t *testing.T) {
	repo, ctx := newTestRepo(t)

	if n, err := repo.Count(ctx); err != nil || n != 0 {
		t.Fatalf("Count() on an empty collection = %d, %v; want 0, nil", n, err)
	}
	for i := range 3 {
		if _, err := repo.Create(ctx, sample(fmt.Sprintf("c%d@example.com", i))); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	n, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if n != 3 {
		t.Errorf("Count() = %d, want 3", n)
	}
}

package seller

import "context"

// Repository is the persistence port, declared by the consumer.
type Repository interface {
	// Create returns ErrShopNameTaken or ErrAlreadySeller, both derived from
	// unique indexes rather than from preceding reads.
	Create(ctx context.Context, s Seller) (Seller, error)

	ByID(ctx context.Context, id string) (Seller, error)
	ByUserID(ctx context.Context, userID string) (Seller, error)
	List(ctx context.Context, limit, offset int) (sellers []Seller, total int, err error)
	Update(ctx context.Context, id string, upd Update) (Seller, error)
}

// Update carries only the fields the caller supplied; nil means leave alone.
type Update struct {
	ShopName *string
	Status   *Status
}

// IsEmpty reports whether the update would change nothing.
func (u Update) IsEmpty() bool { return u.ShopName == nil && u.Status == nil }

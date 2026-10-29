package domain

import "context"

// All repositories take the transaction from ctx (platform-go/mongox) and return domain errors.

// ListQuery selects a page of products.
type ListQuery struct {
	Page, Limit int
	Sort        string // "ctime" (newest first, default), "price_asc", "price_desc"
}

// ProductRepository stores products.
type ProductRepository interface {
	Create(ctx context.Context, p *Product) error // ErrProductNameTaken
	FindByID(ctx context.Context, id string) (*Product, error)
	FindByIDs(ctx context.Context, ids []string) ([]*Product, error) // unknown ids are skipped
	Update(ctx context.Context, p *Product) error
	ListPublished(ctx context.Context, q ListQuery) ([]*Product, int64, error)
	Search(ctx context.Context, keyword string, page, limit int) ([]*Product, int64, error) // published only, best match first
	ListByShop(ctx context.Context, shopID string, published bool, page, limit int) ([]*Product, int64, error)
	IDsByShop(ctx context.Context, shopID string) ([]string, error) // published products
}

// SpuRepository stores SPUs and their SKUs.
type SpuRepository interface {
	Create(ctx context.Context, spu *Spu, skus []*Sku) error // ErrSpuNameTaken
	FindByID(ctx context.Context, id string) (*Spu, error)
	SkusOf(ctx context.Context, spuID string) ([]*Sku, error)
	FindSku(ctx context.Context, skuID, spuID string) (*Sku, error) // ErrSkuNotFound
}

// CommentRepository stores the comment trees.
type CommentRepository interface {
	FindByID(ctx context.Context, id string) (*Comment, error)
	// AddRoot appends a top-level comment, assigning Left / Right.
	AddRoot(ctx context.Context, c *Comment) error
	// AddReply inserts c as the last child of parent, shifting the nodes to its right.
	AddReply(ctx context.Context, parent, c *Comment) error
	// DeleteSubtree removes c with all its descendants and closes the gap.
	DeleteSubtree(ctx context.Context, c *Comment) error
	// List returns the direct children of parentID ("" = top level) by Left, with paging.
	List(ctx context.Context, productID, parentID string, limit, skip int) ([]*Comment, error)
}

// ShopRepository stores the shop projection.
type ShopRepository interface {
	Upsert(ctx context.Context, s ShopProjection) error
	FindByIDs(ctx context.Context, ids []string) (map[string]ShopProjection, error)
}

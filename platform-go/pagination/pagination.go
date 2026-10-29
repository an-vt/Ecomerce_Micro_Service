// Package pagination normalises page / limit query parameters (docs/05-api-design.md).
package pagination

const (
	DefaultLimit = 50
	MaxLimit     = 100
)

// Page is a normalised page request. Page is 1-based.
type Page struct {
	Page  int
	Limit int
}

// New clamps the inputs: page >= 1, 1 <= limit <= MaxLimit (0 means DefaultLimit).
func New(page, limit int) Page {
	if page < 1 {
		page = 1
	}
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	return Page{Page: page, Limit: limit}
}

// Offset is the number of rows to skip.
func (p Page) Offset() int { return (p.Page - 1) * p.Limit }

// Result is a page of items with the total count.
type Result[T any] struct {
	Items []T
	Total int64
	Page  int
	Limit int
}

// NewResult builds a Result for p.
func NewResult[T any](p Page, items []T, total int64) Result[T] {
	if items == nil {
		items = []T{}
	}
	return Result[T]{Items: items, Total: total, Page: p.Page, Limit: p.Limit}
}

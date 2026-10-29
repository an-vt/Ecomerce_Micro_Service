package pagination_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/an-vt/ecom/platform-go/pagination"
)

func TestNewClamps(t *testing.T) {
	tests := []struct {
		page, limit int
		want        pagination.Page
		offset      int
	}{
		{0, 0, pagination.Page{Page: 1, Limit: 50}, 0},
		{-3, -1, pagination.Page{Page: 1, Limit: 50}, 0},
		{3, 20, pagination.Page{Page: 3, Limit: 20}, 40},
		{2, 1000, pagination.Page{Page: 2, Limit: 100}, 100},
	}
	for _, tt := range tests {
		got := pagination.New(tt.page, tt.limit)
		assert.Equal(t, tt.want, got)
		assert.Equal(t, tt.offset, got.Offset())
	}
}

func TestNewResultNeverNilItems(t *testing.T) {
	r := pagination.NewResult[string](pagination.New(1, 10), nil, 0)
	assert.NotNil(t, r.Items)
	assert.Empty(t, r.Items)
	r2 := pagination.NewResult(pagination.New(2, 5), []int{1, 2}, 12)
	assert.Equal(t, int64(12), r2.Total)
	assert.Equal(t, 2, r2.Page)
	assert.Equal(t, 5, r2.Limit)
}

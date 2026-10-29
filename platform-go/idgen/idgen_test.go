package idgen_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/idgen"
)

func TestULIDIsValidAndUnique(t *testing.T) {
	var g idgen.Generator = idgen.ULID{}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := g.New()
		_, err := ulid.ParseStrict(id)
		require.NoError(t, err)
		require.False(t, seen[id], "duplicate id")
		seen[id] = true
	}
}

func TestUUIDIsV4(t *testing.T) {
	id, err := uuid.Parse(idgen.UUID{}.New())
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(4), id.Version())
}

func TestSequence(t *testing.T) {
	s := &idgen.Sequence{Prefix: "id-"}
	assert.Equal(t, "id-1", s.New())
	assert.Equal(t, "id-2", s.New())
	for i := 0; i < 8; i++ {
		s.New()
	}
	assert.Equal(t, "id-11", s.New())
}

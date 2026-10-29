// Package idgen generates identifiers: ULIDs for messages and Mongo documents (sortable, URL-safe) and
// UUIDs for PostgreSQL rows (docs/04-data-model.md).
package idgen

import (
	"crypto/rand"
	"time"

	"github.com/google/uuid"
	"github.com/oklog/ulid/v2"
)

// Generator creates new identifiers as strings.
type Generator interface {
	New() string
}

// ULID generates ULIDs.
type ULID struct{}

// New implements Generator.
func (ULID) New() string { return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String() }

// UUID generates random (v4) UUIDs.
type UUID struct{}

// New implements Generator.
func (UUID) New() string { return uuid.NewString() }

// Sequence returns "<prefix>1", "<prefix>2", … for deterministic tests. Not safe for concurrent use.
type Sequence struct {
	Prefix string
	n      int
}

// New implements Generator.
func (s *Sequence) New() string {
	s.n++
	return s.Prefix + itoa(s.n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

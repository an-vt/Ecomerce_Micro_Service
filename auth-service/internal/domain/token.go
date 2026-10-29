package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashToken returns the hex SHA-256 of an opaque secret (refresh token, one-time token, API key). Only hashes are
// stored, so a database leak does not leak usable credentials.
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

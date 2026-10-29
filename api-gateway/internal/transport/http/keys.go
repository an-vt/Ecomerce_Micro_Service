package http

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashKey is the SHA-256 hex of an API key: what is cached, never the key.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

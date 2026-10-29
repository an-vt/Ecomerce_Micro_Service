// Package security holds the cryptographic adapters of auth-service: bcrypt password hashing and the generator of
// opaque random secrets (docs/07-auth-security.md).
package security

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/an-vt/ecom/auth-service/internal/domain"
)

// Bcrypt implements usecase.PasswordHasher.
type Bcrypt struct{ Cost int }

// NewBcrypt validates the cost (4..31); the default is 10 (docs/07 §6).
func NewBcrypt(cost int) (Bcrypt, error) {
	if cost == 0 {
		cost = 10
	}
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return Bcrypt{}, fmt.Errorf("bcrypt cost %d out of range %d..%d", cost, bcrypt.MinCost, bcrypt.MaxCost)
	}
	return Bcrypt{Cost: cost}, nil
}

// Hash returns the bcrypt hash of plain.
func (b Bcrypt) Hash(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), b.Cost)
	return string(h), err
}

// Compare returns nil when plain matches hash.
func (b Bcrypt) Compare(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}

// Secrets implements usecase.SecretGenerator: 32 random bytes, base64url, stored as a SHA-256 hash.
type Secrets struct{}

// New returns a fresh secret and the hash to store.
func (Secrets) New() (secret, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("random secret: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(b)
	return secret, domain.HashToken(secret), nil
}

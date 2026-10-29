// Package jwtx signs and verifies the RS256 access tokens (docs/07-auth-security.md §2). auth-service holds the
// private key and publishes the public key as a JWKS; the gateway verifies tokens locally and refetches the JWKS
// when it meets an unknown key id (key rotation) — no database hit per request.
package jwtx

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Error codes.
const (
	CodeTokenInvalid = "TOKEN_INVALID"
	CodeTokenExpired = "TOKEN_EXPIRED"
)

// Claims of an access token.
type Claims struct {
	jwt.RegisteredClaims
	Type      string   `json:"typ"` // SHOP | USER
	Email     string   `json:"email,omitempty"`
	Roles     []string `json:"roles,omitempty"`
	SessionID string   `json:"sid"`
}

// AccountID is the subject.
func (c Claims) AccountID() string { return c.Subject }

// JWK is one public key of a JWKS.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS is a key set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// ParsePrivateKey reads a PEM RSA private key (PKCS#1 or PKCS#8).
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("jwtx: no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("jwtx: parse private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("jwtx: private key is not RSA")
	}
	return rk, nil
}

// Signer issues tokens.
type Signer struct {
	key      *rsa.PrivateKey
	kid      string
	issuer   string
	audience string
	ttl      time.Duration
	clk      clock.Clock
}

// NewSigner creates a signer. The key id is derived from the public key, so rotating the key changes it.
func NewSigner(key *rsa.PrivateKey, issuer, audience string, ttl time.Duration, clk clock.Clock) *Signer {
	if clk == nil {
		clk = clock.System{}
	}
	return &Signer{key: key, kid: keyID(&key.PublicKey), issuer: issuer, audience: audience, ttl: ttl, clk: clk}
}

func keyID(pub *rsa.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub.N.Bytes()[:12])
}

// Kid is the id of the signing key.
func (s *Signer) Kid() string { return s.kid }

// TTL is the lifetime of issued tokens.
func (s *Signer) TTL() time.Duration { return s.ttl }

// Sign issues an access token for an account session.
func (s *Signer) Sign(accountID, accountType, email string, roles []string, sessionID string) (string, time.Time, error) {
	now := s.clk.Now()
	exp := now.Add(s.ttl)
	c := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: accountID, Issuer: s.issuer, Audience: jwt.ClaimStrings{s.audience},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp), ID: sessionID + ":" + fmt.Sprint(now.UnixNano()),
		},
		Type: accountType, Email: email, Roles: roles, SessionID: sessionID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = s.kid
	signed, err := tok.SignedString(s.key)
	return signed, exp, err
}

// JWKS publishes the verification key.
func (s *Signer) JWKS() JWKS {
	pub := s.key.PublicKey
	return JWKS{Keys: []JWK{{
		Kty: "RSA", Kid: s.kid, Alg: "RS256", Use: "sig",
		N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}}
}

// Fetcher loads the current JWKS (a gRPC call to auth-service).
type Fetcher func(ctx context.Context) (JWKS, error)

// Verifier checks tokens against a cached key set.
type Verifier struct {
	issuer, audience string
	clk              clock.Clock
	fetch            Fetcher
	minRefresh       time.Duration

	mu          sync.RWMutex
	keys        map[string]*rsa.PublicKey
	lastRefresh time.Time
}

// NewVerifier creates a verifier. fetch may be nil when keys are installed with SetKeys. Unknown key ids trigger
// at most one refetch per minRefresh (default 10 s), so a flood of forged tokens cannot hammer auth-service.
func NewVerifier(issuer, audience string, clk clock.Clock, fetch Fetcher) *Verifier {
	if clk == nil {
		clk = clock.System{}
	}
	return &Verifier{issuer: issuer, audience: audience, clk: clk, fetch: fetch, minRefresh: 10 * time.Second, keys: map[string]*rsa.PublicKey{}}
}

// SetKeys replaces the cached key set.
func (v *Verifier) SetKeys(set JWKS) error {
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		pub, err := k.publicKey()
		if err != nil {
			return err
		}
		keys[k.Kid] = pub
	}
	v.mu.Lock()
	v.keys = keys
	v.mu.Unlock()
	return nil
}

func (k JWK) publicKey() (*rsa.PublicKey, error) {
	if k.Kty != "RSA" {
		return nil, fmt.Errorf("jwtx: unsupported key type %q", k.Kty)
	}
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("jwtx: bad modulus: %w", err)
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("jwtx: bad exponent: %w", err)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
}

func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	k, ok := v.keys[kid]
	v.mu.RUnlock()
	if ok {
		return k, nil
	}
	if v.fetch == nil {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	v.mu.Lock()
	if now := v.clk.Now(); now.Sub(v.lastRefresh) < v.minRefresh {
		v.mu.Unlock()
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	v.lastRefresh = v.clk.Now()
	v.mu.Unlock()

	set, err := v.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("refresh jwks: %w", err)
	}
	if err := v.SetKeys(set); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

// Verify checks signature, algorithm (RS256 only), issuer, audience and expiry, and returns the claims.
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token has no key id")
		}
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(v.issuer), jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(), jwt.WithTimeFunc(v.clk.Now))
	switch {
	case err == nil:
		if c.Subject == "" || c.SessionID == "" {
			return Claims{}, errs.Unauthenticated(CodeTokenInvalid, "token is missing subject or session")
		}
		return c, nil
	case errors.Is(err, jwt.ErrTokenExpired):
		return Claims{}, errs.Unauthenticated(CodeTokenExpired, "access token expired")
	}
	return Claims{}, errs.Unauthenticated(CodeTokenInvalid, "access token is invalid")
}

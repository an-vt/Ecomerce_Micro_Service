package jwtx_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/jwtx"
)

var start = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return k
}

func pair(t *testing.T) (*jwtx.Signer, *jwtx.Verifier, *clock.Fixed) {
	t.Helper()
	clk := clock.NewFixed(start)
	s := jwtx.NewSigner(newKey(t), "ecom-auth", "ecom", 15*time.Minute, clk)
	v := jwtx.NewVerifier("ecom-auth", "ecom", clk, nil)
	require.NoError(t, v.SetKeys(s.JWKS()))
	return s, v, clk
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	s, v, _ := pair(t)
	tok, exp, err := s.Sign("acc-1", "SHOP", "a@b.c", []string{"shop"}, "sid-1")
	require.NoError(t, err)
	assert.Equal(t, start.Add(15*time.Minute), exp)
	assert.Equal(t, 15*time.Minute, s.TTL())

	c, err := v.Verify(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "acc-1", c.AccountID())
	assert.Equal(t, "SHOP", c.Type)
	assert.Equal(t, "a@b.c", c.Email)
	assert.Equal(t, []string{"shop"}, c.Roles)
	assert.Equal(t, "sid-1", c.SessionID)
}

func TestExpiredTokenIsReportedAsExpired(t *testing.T) {
	s, v, clk := pair(t)
	tok, _, _ := s.Sign("acc-1", "USER", "", nil, "sid-1")
	clk.Advance(14 * time.Minute)
	_, err := v.Verify(context.Background(), tok)
	require.NoError(t, err, "still valid just before expiry")
	clk.Advance(2 * time.Minute)
	_, err = v.Verify(context.Background(), tok)
	require.Error(t, err)
	assert.Equal(t, jwtx.CodeTokenExpired, errs.CodeOf(err))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
}

func TestInvalidTokensAreRejected(t *testing.T) {
	s, v, clk := pair(t)
	good, _, _ := s.Sign("acc-1", "USER", "", nil, "sid-1")
	ctx := context.Background()

	otherIssuer := jwtx.NewSigner(newKey(t), "evil", "ecom", time.Hour, clk)
	forged, _, _ := otherIssuer.Sign("acc-1", "USER", "", nil, "sid-1")
	wrongAud := jwtx.NewSigner(newKey(t), "ecom-auth", "other", time.Hour, clk)
	wa, _, _ := wrongAud.Sign("acc-1", "USER", "", nil, "sid-1")
	noSession, _, _ := s.Sign("acc-1", "USER", "", nil, "")
	noSubject, _, _ := s.Sign("", "USER", "", nil, "sid")

	parts := strings.Split(good, ".")
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]

	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "acc-1", Issuer: "ecom-auth", Audience: jwt.ClaimStrings{"ecom"}, ExpiresAt: jwt.NewNumericDate(start.Add(time.Hour))})
	hs.Header["kid"] = s.Kid()
	hsTok, _ := hs.SignedString([]byte("secret"))
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{Subject: "acc-1", Issuer: "ecom-auth", Audience: jwt.ClaimStrings{"ecom"}, ExpiresAt: jwt.NewNumericDate(start.Add(time.Hour))})
	none.Header["kid"] = s.Kid()
	noneTok, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)

	for name, tok := range map[string]string{
		"garbage": "not.a.jwt", "empty": "", "tampered payload": tampered, "signed by another key": forged,
		"wrong audience": wa, "missing session": noSession, "missing subject": noSubject,
		"HS256 confusion": hsTok, "alg none": noneTok,
	} {
		_, err := v.Verify(ctx, tok)
		require.Error(t, err, name)
		assert.Equal(t, jwtx.CodeTokenInvalid, errs.CodeOf(err), name)
	}
}

func TestUnknownKidRefetchesJWKSOnceAndSupportsRotation(t *testing.T) {
	clk := clock.NewFixed(start)
	oldS := jwtx.NewSigner(newKey(t), "ecom-auth", "ecom", time.Hour, clk)
	newS := jwtx.NewSigner(newKey(t), "ecom-auth", "ecom", time.Hour, clk)
	require.NotEqual(t, oldS.Kid(), newS.Kid())

	fetches := 0
	published := oldS.JWKS()
	v := jwtx.NewVerifier("ecom-auth", "ecom", clk, func(context.Context) (jwtx.JWKS, error) { fetches++; return published, nil })

	oldTok, _, _ := oldS.Sign("a", "USER", "", nil, "s")
	_, err := v.Verify(context.Background(), oldTok)
	require.NoError(t, err, "empty cache: the first token triggers the initial fetch")
	assert.Equal(t, 1, fetches)

	// auth rotates the key: tokens of both keys are valid during the overlap
	published = jwtx.JWKS{Keys: append(oldS.JWKS().Keys, newS.JWKS().Keys...)}
	newTok, _, _ := newS.Sign("a", "USER", "", nil, "s")
	clk.Advance(11 * time.Second)
	_, err = v.Verify(context.Background(), newTok)
	require.NoError(t, err)
	assert.Equal(t, 2, fetches)
	_, err = v.Verify(context.Background(), oldTok)
	require.NoError(t, err)
	assert.Equal(t, 2, fetches, "known keys are served from the cache")
}

func TestForgedKidsCannotHammerTheAuthService(t *testing.T) {
	clk := clock.NewFixed(start)
	fetches := 0
	v := jwtx.NewVerifier("ecom-auth", "ecom", clk, func(context.Context) (jwtx.JWKS, error) { fetches++; return jwtx.JWKS{}, nil })
	attacker := jwtx.NewSigner(newKey(t), "ecom-auth", "ecom", time.Hour, clk)
	tok, _, _ := attacker.Sign("a", "USER", "", nil, "s")
	for i := 0; i < 50; i++ {
		_, err := v.Verify(context.Background(), tok)
		assert.Error(t, err)
	}
	assert.Equal(t, 1, fetches, "refetch is rate limited")
	clk.Advance(11 * time.Second)
	_, _ = v.Verify(context.Background(), tok)
	assert.Equal(t, 2, fetches)
}

func TestFetchErrorsAndBadKeys(t *testing.T) {
	clk := clock.NewFixed(start)
	s := jwtx.NewSigner(newKey(t), "ecom-auth", "ecom", time.Hour, clk)
	tok, _, _ := s.Sign("a", "USER", "", nil, "s")

	v := jwtx.NewVerifier("ecom-auth", "ecom", clk, func(context.Context) (jwtx.JWKS, error) { return jwtx.JWKS{}, errors.New("auth down") })
	_, err := v.Verify(context.Background(), tok)
	assert.Equal(t, jwtx.CodeTokenInvalid, errs.CodeOf(err), "an unreachable auth service never lets a token through")

	bad := jwtx.NewVerifier("i", "a", clk, nil)
	assert.Error(t, bad.SetKeys(jwtx.JWKS{Keys: []jwtx.JWK{{Kty: "EC", Kid: "x"}}}))
	assert.Error(t, bad.SetKeys(jwtx.JWKS{Keys: []jwtx.JWK{{Kty: "RSA", Kid: "x", N: "!!!", E: "AQAB"}}}))
	assert.Error(t, bad.SetKeys(jwtx.JWKS{Keys: []jwtx.JWK{{Kty: "RSA", Kid: "x", N: "AQAB", E: "!!!"}}}))

	// set that never contains the kid after a successful fetch
	v2 := jwtx.NewVerifier("ecom-auth", "ecom", clk, func(context.Context) (jwtx.JWKS, error) { return jwtx.JWKS{}, nil })
	_, err = v2.Verify(context.Background(), tok)
	assert.Error(t, err)
	v3 := jwtx.NewVerifier("ecom-auth", "ecom", clk, func(context.Context) (jwtx.JWKS, error) { return jwtx.JWKS{Keys: []jwtx.JWK{{Kty: "EC"}}}, nil })
	_, err = v3.Verify(context.Background(), tok)
	assert.Error(t, err)

	noKid := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{Subject: "a"})
	signed, _ := noKid.SignedString(newKey(t))
	_, err = jwtx.NewVerifier("ecom-auth", "ecom", clk, nil).Verify(context.Background(), signed)
	assert.Error(t, err)
}

func TestParsePrivateKeyPKCS1AndPKCS8(t *testing.T) {
	k := newKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	got, err := jwtx.ParsePrivateKey(pkcs1)
	require.NoError(t, err)
	assert.True(t, got.Equal(k))

	der, err := x509.MarshalPKCS8PrivateKey(k)
	require.NoError(t, err)
	got, err = jwtx.ParsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	require.NoError(t, err)
	assert.True(t, got.Equal(k))

	_, err = jwtx.ParsePrivateKey([]byte("nope"))
	assert.ErrorContains(t, err, "no PEM block")
	_, err = jwtx.ParsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")}))
	assert.Error(t, err)
}

func TestJWKSShapeAndDefaultClock(t *testing.T) {
	s := jwtx.NewSigner(newKey(t), "i", "a", time.Minute, nil)
	set := s.JWKS()
	require.Len(t, set.Keys, 1)
	assert.Equal(t, "RSA", set.Keys[0].Kty)
	assert.Equal(t, "RS256", set.Keys[0].Alg)
	assert.Equal(t, "AQAB", set.Keys[0].E)
	assert.Equal(t, s.Kid(), set.Keys[0].Kid)
	v := jwtx.NewVerifier("i", "a", nil, nil)
	require.NoError(t, v.SetKeys(set))
	tok, _, err := s.Sign("x", "USER", "", nil, "sid")
	require.NoError(t, err)
	_, err = v.Verify(context.Background(), tok)
	assert.NoError(t, err)
}

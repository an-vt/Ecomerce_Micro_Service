package s3x_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/s3x"
)

const (
	awsAccess = "AKIAIOSFODNN7EXAMPLE"
	awsSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

// The example of "Authenticating Requests: Using Query Parameters (AWS Signature Version 4)" in the S3 documentation.
// If this passes, the canonical request, the string to sign and the key derivation are what AWS computes.
func TestReproducesTheAWSDocumentationExample(t *testing.T) {
	s, err := s3x.New(s3x.Config{Endpoint: "https://s3.amazonaws.com", Region: "us-east-1", AccessKey: awsAccess, SecretKey: awsSecret})
	require.NoError(t, err)
	got, err := s.Presign(s3x.Request{Method: "GET", Bucket: "examplebucket", Key: "test.txt", Expires: 86400 * time.Second,
		Now: time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	assert.Equal(t, "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256"+
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z"+
		"&X-Amz-Expires=86400&X-Amz-SignedHeaders=host"+
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404", got)
}

func signer(t *testing.T, mut func(*s3x.Config)) *s3x.Signer {
	t.Helper()
	c := s3x.Config{Endpoint: "http://localhost:9000", AccessKey: "ak", SecretKey: "sk", PathStyle: true}
	if mut != nil {
		mut(&c)
	}
	s, err := s3x.New(c)
	require.NoError(t, err)
	return s
}

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func parse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func TestPathStyleKeepsTheBucketInThePathAndThePortInTheHost(t *testing.T) {
	u := parse(t, mustPresign(t, signer(t, nil), s3x.Request{Method: "PUT", Bucket: "media", Key: "products/s1/a b.jpg", Expires: 5 * time.Minute, Now: now}))
	assert.Equal(t, "http", u.Scheme)
	assert.Equal(t, "localhost:9000", u.Host)
	assert.Equal(t, "/media/products/s1/a b.jpg", u.Path)
	assert.Equal(t, "/media/products/s1/a%20b.jpg", u.EscapedPath(), "a space is %20 in the path, as the signature assumed")
	q := u.Query()
	assert.Equal(t, "AWS4-HMAC-SHA256", q.Get("X-Amz-Algorithm"))
	assert.Equal(t, "ak/20261030/us-east-1/s3/aws4_request", q.Get("X-Amz-Credential"))
	assert.Equal(t, "20261030T120000Z", q.Get("X-Amz-Date"))
	assert.Equal(t, "300", q.Get("X-Amz-Expires"))
	assert.Equal(t, "host", q.Get("X-Amz-SignedHeaders"))
	assert.Len(t, q.Get("X-Amz-Signature"), 64)
}

func TestVirtualHostedStyleAndDefaultPorts(t *testing.T) {
	s := signer(t, func(c *s3x.Config) { c.Endpoint = "https://storage.example.com:443"; c.PathStyle = false })
	u := parse(t, mustPresign(t, s, s3x.Request{Method: "GET", Bucket: "media", Key: "a.jpg", Expires: time.Minute, Now: now}))
	assert.Equal(t, "media.storage.example.com", u.Host, "the bucket is in the host and :443 is dropped, like the Host header")
	assert.Equal(t, "/a.jpg", u.Path)
}

func TestTheSignatureCoversTheRequestSoNothingCanBeChanged(t *testing.T) {
	s := signer(t, nil)
	base := s3x.Request{Method: "PUT", Bucket: "media", Key: "k.jpg", Expires: time.Minute, Now: now, Headers: map[string]string{"Content-Type": "image/jpeg", "Content-Length": "1234"}}
	sigOf := func(r s3x.Request) string { return parse(t, mustPresign(t, s, r)).Query().Get("X-Amz-Signature") }
	ref := sigOf(base)
	assert.Equal(t, ref, sigOf(base), "deterministic")

	vary := map[string]func(*s3x.Request){
		"method": func(r *s3x.Request) { r.Method = "GET" },
		"bucket": func(r *s3x.Request) { r.Bucket = "other" },
		"key":    func(r *s3x.Request) { r.Key = "other.jpg" },
		"expiry": func(r *s3x.Request) { r.Expires = 2 * time.Minute },
		"time":   func(r *s3x.Request) { r.Now = now.Add(time.Second) },
		"content type": func(r *s3x.Request) {
			r.Headers = map[string]string{"Content-Type": "image/png", "Content-Length": "1234"}
		},
		"size": func(r *s3x.Request) {
			r.Headers = map[string]string{"Content-Type": "image/jpeg", "Content-Length": "99999"}
		},
		"no headers": func(r *s3x.Request) { r.Headers = nil },
	}
	for name, mut := range vary {
		r := base
		mut(&r)
		assert.NotEqual(t, ref, sigOf(r), name+" is part of the signature")
	}
	other := signer(t, func(c *s3x.Config) { c.SecretKey = "different" })
	assert.NotEqual(t, ref, parse(t, mustPresign(t, other, base)).Query().Get("X-Amz-Signature"))
}

func TestSignedHeadersAreListedSortedAndNormalised(t *testing.T) {
	u := parse(t, mustPresign(t, signer(t, nil), s3x.Request{Method: "PUT", Bucket: "b", Key: "k", Expires: time.Minute, Now: now,
		Headers: map[string]string{"Content-Type": "  image/png ", "Content-Length": "10"}}))
	assert.Equal(t, "content-length;content-type;host", u.Query().Get("X-Amz-SignedHeaders"))
	same := parse(t, mustPresign(t, signer(t, nil), s3x.Request{Method: "put", Bucket: "b", Key: "k", Expires: time.Minute, Now: now,
		Headers: map[string]string{"content-length": "10", "CONTENT-TYPE": "image/png"}}))
	assert.Equal(t, u.Query().Get("X-Amz-Signature"), same.Query().Get("X-Amz-Signature"), "header name case, surrounding space and method case do not matter")
}

func TestKeysWithSpecialCharactersAreEncodedTheAWSWay(t *testing.T) {
	u := mustPresign(t, signer(t, nil), s3x.Request{Method: "GET", Bucket: "b", Key: "a+b&c=d,é ~.jpg", Expires: time.Minute, Now: now})
	assert.Contains(t, u, "/b/a%2Bb%26c%3Dd%2C%C3%A9%20~.jpg?", "+ & = , and non-ASCII are percent-encoded; ~ and . are not")
}

func TestObjectURL(t *testing.T) {
	assert.Equal(t, "http://localhost:9000/media/products/s1/a.jpg", signer(t, nil).ObjectURL("media", "products/s1/a.jpg"))
	assert.Equal(t, "https://media.s3.amazonaws.com/a.jpg", signer(t, func(c *s3x.Config) { c.Endpoint = "https://s3.amazonaws.com"; c.PathStyle = false }).ObjectURL("media", "a.jpg"))
}

func TestValidation(t *testing.T) {
	for _, c := range []s3x.Config{
		{Endpoint: "", AccessKey: "a", SecretKey: "s"}, {Endpoint: "ftp://x", AccessKey: "a", SecretKey: "s"}, {Endpoint: "localhost:9000", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "http://x", AccessKey: "", SecretKey: "s"}, {Endpoint: "http://x", AccessKey: "a", SecretKey: ""},
	} {
		_, err := s3x.New(c)
		assert.Error(t, err, "%+v", c)
	}
	s := signer(t, nil)
	for name, r := range map[string]s3x.Request{
		"no method":  {Bucket: "b", Key: "k", Expires: time.Minute},
		"no bucket":  {Method: "GET", Key: "k", Expires: time.Minute},
		"no expiry":  {Method: "GET", Bucket: "b", Key: "k"},
		"too long":   {Method: "GET", Bucket: "b", Key: "k", Expires: 8 * 24 * time.Hour},
		"sub-second": {Method: "GET", Bucket: "b", Key: "k", Expires: 500 * time.Millisecond},
	} {
		r.Now = now
		_, err := s.Presign(r)
		assert.Error(t, err, name)
	}
	assert.False(t, strings.Contains(mustPresign(t, s, s3x.Request{Method: "GET", Bucket: "b", Key: "k", Expires: 7 * 24 * time.Hour, Now: now}), "sk"), "the secret never appears in a URL")
}

func mustPresign(t *testing.T, s *s3x.Signer, r s3x.Request) string {
	t.Helper()
	u, err := s.Presign(r)
	require.NoError(t, err)
	return u
}

func TestBucketLevelRequests(t *testing.T) {
	u := parse(t, mustPresign(t, signer(t, nil), s3x.Request{Method: "PUT", Bucket: "media", Expires: time.Minute, Now: now}))
	assert.Equal(t, "/media", u.Path, "path style: the bucket alone")
	v := parse(t, mustPresign(t, signer(t, func(c *s3x.Config) { c.PathStyle = false }), s3x.Request{Method: "PUT", Bucket: "media", Expires: time.Minute, Now: now}))
	assert.Equal(t, "media.localhost:9000", v.Host)
	assert.Equal(t, "/", v.Path, "virtual-hosted style: the root")
}

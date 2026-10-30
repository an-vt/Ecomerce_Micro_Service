// Package s3x creates presigned URLs for S3-compatible object stores (AWS S3, MinIO, Cloudflare R2, Scality...) with
// AWS Signature Version 4 in the query string. It only signs: it never talks to the store, so a service that hands out
// upload URLs needs no SDK and no network access to the store.
//
// Reference: "Authenticating Requests: Using Query Parameters (AWS Signature Version 4)". The tests reproduce the
// example in that document byte for byte.
package s3x

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm   = "AWS4-HMAC-SHA256"
	service     = "s3"
	maxExpiry   = 7 * 24 * time.Hour // the limit of SigV4
	unsignedPay = "UNSIGNED-PAYLOAD"
)

// Config of the store.
type Config struct {
	// Endpoint is the URL clients use to reach the store, e.g. https://s3.amazonaws.com or http://localhost:9000. The
	// signature covers the host, so it must be the host the client will really call.
	Endpoint  string
	Region    string // default us-east-1
	AccessKey string
	SecretKey string
	// PathStyle puts the bucket in the path (endpoint/bucket/key), which MinIO and most compatible stores need;
	// otherwise it goes in the host (bucket.endpoint/key), which is what AWS S3 prefers.
	PathStyle bool
}

// Signer creates presigned URLs.
type Signer struct {
	c    Config
	base *url.URL
}

// New validates the configuration.
func New(c Config) (*Signer, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("s3x: endpoint %q is not an http(s) URL", c.Endpoint)
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return nil, errors.New("s3x: access key and secret key are required")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	return &Signer{c: c, base: u}, nil
}

// Request describes the call a presigned URL allows.
type Request struct {
	Method  string // GET, PUT, ...
	Bucket  string
	Key     string // empty for a bucket-level request (create the bucket, list it)
	Expires time.Duration
	// Headers the caller must send with exactly these values (Content-Type, Content-Length...). They are part of the
	// signature, so the store rejects a request that sends others. "host" is always signed and need not be listed.
	Headers map[string]string
	Now     time.Time
}

// Presign returns a URL that allows exactly the described request until it expires.
func (s *Signer) Presign(r Request) (string, error) {
	if r.Method == "" || r.Bucket == "" {
		return "", errors.New("s3x: method and bucket are required")
	}
	if r.Expires < time.Second || r.Expires > maxExpiry {
		return "", fmt.Errorf("s3x: expiry must be between 1 second and %s", maxExpiry)
	}
	now := r.Now.UTC()
	date, stamp := now.Format("20060102"), now.Format("20060102T150405Z")
	scope := strings.Join([]string{date, s.c.Region, service, "aws4_request"}, "/")

	host, path := s.hostAndPath(r.Bucket, r.Key)
	headers := map[string]string{"host": host}
	for k, v := range r.Headers {
		headers[strings.ToLower(strings.TrimSpace(k))] = strings.Join(strings.Fields(v), " ")
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")

	query := map[string]string{
		"X-Amz-Algorithm":     algorithm,
		"X-Amz-Credential":    s.c.AccessKey + "/" + scope,
		"X-Amz-Date":          stamp,
		"X-Amz-Expires":       fmt.Sprint(int64(r.Expires / time.Second)),
		"X-Amz-SignedHeaders": signed,
	}
	canonQuery := encodeQuery(query)

	canonical := strings.Join([]string{strings.ToUpper(r.Method), path, canonQuery, canonHeaders.String(), signed, unsignedPay}, "\n")
	hash := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{algorithm, stamp, scope, hex.EncodeToString(hash[:])}, "\n")
	sig := hex.EncodeToString(hmacSHA256(s.signingKey(date), toSign))

	return s.base.Scheme + "://" + host + path + "?" + canonQuery + "&X-Amz-Signature=" + sig, nil
}

// ObjectURL is the plain, unsigned address of an object (for a public bucket or a CDN in front of it).
func (s *Signer) ObjectURL(bucket, key string) string {
	host, path := s.hostAndPath(bucket, key)
	return s.base.Scheme + "://" + host + path
}

func (s *Signer) hostAndPath(bucket, key string) (host, path string) {
	host = s.base.Host
	// the Host header omits the default port
	if h := s.base.Hostname(); (s.base.Scheme == "http" && s.base.Port() == "80") || (s.base.Scheme == "https" && s.base.Port() == "443") {
		host = h
	}
	prefix := strings.TrimRight(s.base.Path, "/")
	if s.c.PathStyle {
		if key == "" {
			return host, prefix + "/" + encode(bucket, true)
		}
		return host, prefix + "/" + encode(bucket, true) + "/" + encode(key, true)
	}
	return bucket + "." + host, prefix + "/" + encode(key, true)
}

func (s *Signer) signingKey(date string) []byte {
	k := hmacSHA256([]byte("AWS4"+s.c.SecretKey), date)
	k = hmacSHA256(k, s.c.Region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// encodeQuery sorts by key and URI-encodes keys and values the AWS way.
func encodeQuery(q map[string]string) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = encode(k, false) + "=" + encode(q[k], false)
	}
	return strings.Join(parts, "&")
}

// encode is AWS's URI encoding: everything except A-Z a-z 0-9 - _ . ~ becomes %XX (uppercase hex); a '/' is kept in
// paths. net/url escapes differently (it leaves $&+,:;=@ alone), which would make the signature differ.
func encode(s string, keepSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~', c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

// Package media adapts the S3-compatible object store to the catalog's MediaStorage port.
package media

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/s3x"
)

// Config of the store as the catalog sees it.
type Config struct {
	Endpoint      string // the address CLIENTS use (the signature covers the host)
	Bucket        string
	Region        string
	AccessKey     string
	SecretKey     string
	PathStyle     bool
	PublicBaseURL string // where uploaded images are served from (a CDN, or the bucket); default: the object URL
}

// Storage implements domain.MediaStorage. It only signs: it never calls the store.
type Storage struct {
	s      *s3x.Signer
	bucket string
	public string
	clk    clock.Clock
}

// New validates the configuration.
func New(c Config, clk clock.Clock) (*Storage, error) {
	if c.Bucket == "" {
		return nil, fmt.Errorf("media: a bucket is required")
	}
	if c.PublicBaseURL != "" {
		if u, err := url.Parse(c.PublicBaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("media: public base URL %q is not an http(s) URL", c.PublicBaseURL)
		}
	}
	s, err := s3x.New(s3x.Config{Endpoint: c.Endpoint, Region: c.Region, AccessKey: c.AccessKey, SecretKey: c.SecretKey, PathStyle: c.PathStyle})
	if err != nil {
		return nil, err
	}
	return &Storage{s: s, bucket: c.Bucket, public: strings.TrimRight(c.PublicBaseURL, "/"), clk: clk}, nil
}

// PresignPut implements domain.MediaStorage. The content type and the exact length are signed: a client that sends
// another type or another size is refused by the store.
func (s *Storage) PresignPut(key, contentType string, size int64, ttl time.Duration) (domain.Upload, error) {
	now := s.clk.Now()
	headers := map[string]string{"Content-Type": contentType, "Content-Length": strconv.FormatInt(size, 10)}
	u, err := s.s.Presign(s3x.Request{Method: "PUT", Bucket: s.bucket, Key: key, Expires: ttl, Headers: headers, Now: now})
	if err != nil {
		return domain.Upload{}, err
	}
	public := s.s.ObjectURL(s.bucket, key)
	if s.public != "" {
		public = s.public + "/" + key
	}
	// Content-Length is set by the HTTP client from the body; only Content-Type has to be sent by hand
	return domain.Upload{URL: u, Method: "PUT", Headers: map[string]string{"Content-Type": contentType}, PublicURL: public, Key: key, ExpiresAt: now.Add(ttl)}, nil
}

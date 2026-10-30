package media_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/catalog-service/internal/client/media"
	"github.com/an-vt/ecom/platform-go/clock"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func cfg() media.Config {
	return media.Config{Endpoint: "http://localhost:8000", Bucket: "ecom-media", AccessKey: "ak", SecretKey: "sk", PathStyle: true}
}

func TestPresignPutSignsTheTypeAndTheExactSize(t *testing.T) {
	st, err := media.New(cfg(), clock.NewFixed(now))
	require.NoError(t, err)
	up, err := st.PresignPut("products/s1/abc.jpg", "image/jpeg", 12345, 5*time.Minute)
	require.NoError(t, err)

	u, err := url.Parse(up.URL)
	require.NoError(t, err)
	assert.Equal(t, "localhost:8000", u.Host)
	assert.Equal(t, "/ecom-media/products/s1/abc.jpg", u.Path)
	assert.Equal(t, "content-length;content-type;host", u.Query().Get("X-Amz-SignedHeaders"))
	assert.Equal(t, "300", u.Query().Get("X-Amz-Expires"))
	assert.Equal(t, "PUT", up.Method)
	assert.Equal(t, map[string]string{"Content-Type": "image/jpeg"}, up.Headers, "the client sends the type; its HTTP stack sets the length")
	assert.Equal(t, "http://localhost:8000/ecom-media/products/s1/abc.jpg", up.PublicURL)
	assert.Equal(t, "products/s1/abc.jpg", up.Key)
	assert.Equal(t, now.Add(5*time.Minute), up.ExpiresAt)

	other, _ := st.PresignPut("products/s1/abc.jpg", "image/jpeg", 99, 5*time.Minute)
	ou, _ := url.Parse(other.URL)
	assert.NotEqual(t, u.Query().Get("X-Amz-Signature"), ou.Query().Get("X-Amz-Signature"), "another size is another signature")
}

func TestThePublicURLCanBeACDN(t *testing.T) {
	c := cfg()
	c.PublicBaseURL = "https://cdn.example.com/media/"
	st, err := media.New(c, clock.NewFixed(now))
	require.NoError(t, err)
	up, err := st.PresignPut("products/s1/abc.png", "image/png", 10, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/media/products/s1/abc.png", up.PublicURL, "uploads go to the store, readers come through the CDN")
	assert.Contains(t, up.URL, "localhost:8000")
}

func TestNewRefusesBadConfiguration(t *testing.T) {
	for name, mut := range map[string]func(*media.Config){
		"no bucket":     func(c *media.Config) { c.Bucket = "" },
		"no endpoint":   func(c *media.Config) { c.Endpoint = "" },
		"no secret":     func(c *media.Config) { c.SecretKey = "" },
		"bad public":    func(c *media.Config) { c.PublicBaseURL = "cdn.example.com" },
		"public scheme": func(c *media.Config) { c.PublicBaseURL = "ftp://cdn.example.com" },
	} {
		c := cfg()
		mut(&c)
		_, err := media.New(c, clock.NewFixed(now))
		assert.Error(t, err, name)
	}
}

func TestAnInvalidTTLIsAnError(t *testing.T) {
	st, _ := media.New(cfg(), clock.NewFixed(now))
	_, err := st.PresignPut("k", "image/png", 1, 0)
	assert.Error(t, err)
}

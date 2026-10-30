//go:build e2e

package e2e_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
	"github.com/an-vt/ecom/platform-go/s3x"
)

// pngBytes is a real (1x1) PNG.
var pngBytes = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0d, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0xfc, 0xcf, 0xc0, 0xf0, 0x1f, 0, 5, 0, 1, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82}

func storeSigner(t *testing.T) *s3x.Signer {
	t.Helper()
	s, err := s3x.New(s3x.Config{Endpoint: e2ekit.Env("MEDIA_ENDPOINT", "http://localhost:8000"), AccessKey: e2ekit.Env("MEDIA_ACCESS_KEY", "ecomaccess"),
		SecretKey: e2ekit.Env("MEDIA_SECRET_KEY", "ecomsecret-change-me"), PathStyle: true})
	require.NoError(t, err)
	return s
}

func put(t *testing.T, url string, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// ensureBucket creates the bucket the catalog signs for (an operator does this once; the catalog only signs).
func ensureBucket(t *testing.T, s *s3x.Signer) {
	t.Helper()
	u, err := s.Presign(s3x.Request{Method: "PUT", Bucket: e2ekit.Env("MEDIA_BUCKET", "ecom-media"), Expires: time.Minute, Now: time.Now()})
	require.NoError(t, err)
	resp := put(t, u, nil, nil)
	require.Contains(t, []int{200, 409}, resp.StatusCode, "create bucket: %d", resp.StatusCode) // 409: it already exists
}

func TestAShopUploadsAProductImageStraightToTheStore(t *testing.T) {
	c := e2ekit.NewClient(t)
	e2ekit.RequireReachable(t, "object store", strings.TrimPrefix(e2ekit.Env("MEDIA_ENDPOINT", "http://localhost:8000"), "http://"))
	ensureBucket(t, storeSigner(t))
	shop := signUpShop(t, c)
	size := len(pngBytes)

	// only shops, and only images
	body := map[string]any{"contentType": "image/png", "size": size}
	assert.Equal(t, 401, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/upload-url", Body: body}).Status)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/upload-url", Token: signUpBuyer(t, c), Body: body}).Status)
	html := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/upload-url", Token: shop.token, Body: map[string]any{"contentType": "text/html", "size": 10}})
	assert.Equal(t, 400, html.Status)
	assert.Equal(t, "UNSUPPORTED_MEDIA_TYPE", html.Code())
	big := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/upload-url", Token: shop.token, Body: map[string]any{"contentType": "image/png", "size": 50 << 20}})
	assert.Equal(t, "FILE_TOO_LARGE", big.Code())

	up := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/upload-url", Token: shop.token, Body: body})
	require.Equal(t, 200, up.Status, string(up.Body))
	uploadURL, publicURL := up.String("metadata.uploadUrl"), up.String("metadata.publicUrl")
	assert.Equal(t, "PUT", up.String("metadata.method"))
	assert.Contains(t, up.String("metadata.key"), "products/"+shop.id+"/", "scoped to the shop that asked")
	headers := map[string]string{"Content-Type": up.String("metadata.headers.Content-Type")}

	// the signature pins the size and the type: the store itself refuses anything else
	assert.Equal(t, 403, put(t, uploadURL, headers, append(append([]byte{}, pngBytes...), 0)).StatusCode, "another size")
	assert.Equal(t, 403, put(t, uploadURL, map[string]string{"Content-Type": "text/html"}, pngBytes).StatusCode, "another type")
	last := uploadURL[len(uploadURL)-1]
	flipped := uploadURL[:len(uploadURL)-1] + map[bool]string{true: "1", false: "0"}[last == '0']
	assert.Equal(t, 403, put(t, flipped, headers, pngBytes).StatusCode, "a tampered signature")

	assert.Equal(t, 200, put(t, uploadURL, headers, pngBytes).StatusCode, "the image the shop declared")

	// the image is where the response said it would be, byte for byte
	signer := storeSigner(t)
	read, err := signer.Presign(s3x.Request{Method: "GET", Bucket: e2ekit.Env("MEDIA_BUCKET", "ecom-media"), Key: up.String("metadata.key"), Expires: time.Minute, Now: time.Now()})
	require.NoError(t, err)
	resp, err := http.Get(read)
	require.NoError(t, err)
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, pngBytes, got)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	assert.NotEmpty(t, publicURL)

	// and it becomes a product's thumb
	created := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product", Token: shop.token, Body: map[string]any{
		"product_type": "Clothing", "product_name": "E2E Pictured Shirt " + time.Now().Format("150405.000000"), "product_thumb": publicURL, "product_price": 1000, "product_quantity": 1,
		"product_attributes": map[string]any{"brand": "b", "size": "M", "material": "c"}}})
	require.Equal(t, 201, created.Status, string(created.Body))
	assert.Equal(t, publicURL, created.String("metadata.product_thumb"))
}

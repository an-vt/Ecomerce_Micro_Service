package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"
)

const docsHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Ecomerce API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css"></head>
<body><div id="ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
<script>window.ui = SwaggerUIBundle({ url: '/docs/openapi.yaml', dom_id: '#ui' });</script>
</body></html>`

func docsPage(c *gin.Context) {
	// the page loads Swagger UI from a CDN, so it needs a relaxed policy of its own
	c.Header("Content-Security-Policy", "default-src 'self' https://unpkg.com; style-src 'self' 'unsafe-inline' https://unpkg.com; script-src 'self' 'unsafe-inline' https://unpkg.com; img-src 'self' data:")
	c.Data(stdhttp.StatusOK, "text/html; charset=utf-8", []byte(docsHTML))
}

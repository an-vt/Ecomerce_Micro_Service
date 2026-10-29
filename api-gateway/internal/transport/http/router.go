package http

import (
	stdhttp "net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/an-vt/ecom/api-gateway/api/openapi"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/httpx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/jwtx"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/rbac"
	"github.com/an-vt/ecom/platform-go/redisx"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
	cartv1 "github.com/an-vt/ecom/proto/gen/go/cart/v1"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
	discountv1 "github.com/an-vt/ecom/proto/gen/go/discount/v1"
	inventoryv1 "github.com/an-vt/ecom/proto/gen/go/inventory/v1"
	notificationv1 "github.com/an-vt/ecom/proto/gen/go/notification/v1"
	orderv1 "github.com/an-vt/ecom/proto/gen/go/order/v1"
)

// Upstreams are the gRPC clients of the services behind the gateway (added phase by phase).
type Upstreams struct {
	Auth         authv1.AuthServiceClient
	Rbac         authv1.RbacServiceClient
	Catalog      catalogv1.CatalogServiceClient
	Inventory    inventoryv1.InventoryServiceClient
	Cart         cartv1.CartServiceClient
	Discount     discountv1.DiscountServiceClient
	Order        orderv1.OrderServiceClient
	Notification notificationv1.NotificationServiceClient
}

// Options is everything NewRouter needs.
type Options struct {
	Upstreams

	Verifier *jwtx.Verifier
	Denylist *redisx.Denylist
	Limiter  *redisx.Limiter
	APICache *redisx.Cache
	Grants   *GrantsCache
	Metrics  *metrics.Metrics
	Checks   map[string]httpx.Check
	IDs      idgen.Generator

	CORSOrigins    []string
	TrustedProxies []string
	CookieSecure   bool
	CookieTTL      time.Duration
	RateLimit      int
	AuthRateLimit  int
	BodyLimit      int64
	APIKeyTTL      time.Duration
	APIKeyNegTTL   time.Duration
}

// PublicPermission is the API-key permission every public route requires.
const PublicPermission = "0000"

// NewRouter builds the Gin engine with the full middleware chain and every route.
func NewRouter(o Options) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	gin.EnableJsonDecoderDisallowUnknownFields()
	if o.IDs == nil {
		o.IDs = idgen.ULID{}
	}
	r := gin.New()
	r.HandleMethodNotAllowed = true
	_ = r.SetTrustedProxies(o.TrustedProxies) // nil: trust no proxy, the client IP is the socket peer

	r.Use(requestID(o.IDs), recoverer(), accessLog(), httpMetrics(o.Metrics.Registry), securityHeaders(o.CookieSecure),
		cors(o.CORSOrigins), bodyLimit(o.BodyLimit))
	r.NoRoute(func(c *gin.Context) { fail(c, errs.NotFound("ROUTE_NOT_FOUND", "no such route")) })
	r.NoMethod(func(c *gin.Context) {
		fail(c, errs.New(errs.KindInvalidArgument, "METHOD_NOT_ALLOWED", "method not allowed"))
	})

	// operational and documentation routes: no API key
	ops := httpx.NewOpsHandler(o.Metrics.Handler(), o.Checks)
	r.GET("/health/live", gin.WrapH(ops))
	r.GET("/health/ready", gin.WrapH(ops))
	r.GET("/metrics", gin.WrapH(ops))
	r.GET("/docs", docsPage)
	r.GET("/docs/openapi.yaml", func(c *gin.Context) { c.Data(stdhttp.StatusOK, "application/yaml", openapi.Spec) })

	window := time.Minute
	v1 := r.Group("/v1/api",
		apiKey(o.Auth, o.APICache, o.APIKeyTTL, o.APIKeyNegTTL, PublicPermission),
		rateLimit(o.Limiter, "all", o.RateLimit, window))
	strict := rateLimit(o.Limiter, "auth", o.AuthRateLimit, window)
	authed := jwtAuth(o.Verifier, o.Denylist)

	auth := &Auth{client: o.Auth, cookieSecure: o.CookieSecure, cookieTTL: o.CookieTTL}
	shop := v1.Group("/shop")
	shop.POST("/signup", strict, auth.signUpShop)
	shop.POST("/login", strict, auth.login(authv1.AccountType_ACCOUNT_TYPE_SHOP))
	shop.POST("/logout", authed, auth.logout)
	shop.POST("/handleRefreshToken", auth.refresh)

	user := v1.Group("/user")
	user.POST("/new_user", strict, auth.newUser)
	user.GET("/welcome_back", strict, auth.welcomeBack)
	user.POST("/password", authed, userOnly(), auth.setPassword)
	user.POST("/login", strict, auth.login(authv1.AccountType_ACCOUNT_TYPE_USER))
	user.POST("/logout", authed, auth.logout)
	user.POST("/handleRefreshToken", auth.refresh)

	admin := &Rbac{client: o.Rbac}
	rb := v1.Group("/rbac", authed)
	rb.POST("/role", requirePermission(o.Grants, "create", "rbac", rbac.Any), admin.createRole)
	rb.GET("/roles", requirePermission(o.Grants, "read", "rbac", rbac.Any), admin.listRoles)
	rb.POST("/resource", requirePermission(o.Grants, "create", "rbac", rbac.Any), admin.createResource)
	rb.GET("/resources", requirePermission(o.Grants, "read", "rbac", rbac.Any), admin.listResources)

	pf := v1.Group("/profile", authed)
	pf.GET("/viewAny", requirePermission(o.Grants, "read", "profile", rbac.Any), auth.viewProfile)
	pf.GET("/viewOwn", requirePermission(o.Grants, "read", "profile", rbac.Own), auth.viewProfile)

	cat := &Catalog{client: o.Catalog}
	pr := v1.Group("/product")
	pr.GET("", cat.list)
	pr.GET("/search/:keySearch", cat.search)
	pr.GET("/spu/get_spu_info", cat.getSpu)
	pr.GET("/sku/select_variation", cat.getSku)
	pr.GET("/drafts/all", authed, shopOnlyGuard(), cat.shopList(false))
	pr.GET("/published/all", authed, shopOnlyGuard(), cat.shopList(true))
	pr.GET("/:productId", cat.get)
	pr.POST("", authed, shopOnlyGuard(), cat.create)
	pr.POST("/spu/new", authed, shopOnlyGuard(), cat.createSpu)
	pr.POST("/publish/:id", authed, shopOnlyGuard(), cat.setPublished(true))
	pr.POST("/unpublish/:id", authed, shopOnlyGuard(), cat.setPublished(false))
	pr.PATCH("/:productId", authed, shopOnlyGuard(), cat.update)

	cm := v1.Group("/comment")
	cm.GET("/list", cat.listComments)
	cm.POST("", authed, cat.createComment)
	cm.DELETE("", authed, cat.deleteComment)

	inv := &Inventory{client: o.Inventory}
	iv := v1.Group("/inventory")
	iv.POST("", authed, shopOnlyGuard(), inv.addStock)
	iv.GET("/:productId", inv.get)

	crt := &Cart{client: o.Cart}
	cr := v1.Group("/cart", authed, userOnly())
	cr.POST("", crt.add)
	cr.POST("/update", crt.update)
	cr.DELETE("", crt.remove)
	cr.GET("", crt.get)

	dsc := &Discount{client: o.Discount}
	ds := v1.Group("/discount")
	ds.GET("/list_product_code", dsc.productsForCode)
	ds.POST("", authed, shopOnlyGuard(), dsc.create)
	ds.PATCH("/:discountId", authed, shopOnlyGuard(), dsc.update)
	ds.GET("", authed, shopOnlyGuard(), dsc.list)
	ds.POST("/amount", authed, userOnly(), dsc.amount)

	ord := &Order{client: o.Order}
	v1.POST("/checkout/review", authed, userOnly(), ord.review)
	od := v1.Group("/orders", authed, userOnly())
	od.POST("", ord.place)
	od.GET("", ord.list)
	od.GET("/:orderId", ord.get)

	nf := &Notification{client: o.Notification}
	v1.GET("/notification", authed, nf.list)
	v1.POST("/email/new_template", authed, requirePermission(o.Grants, "create", "template", rbac.Any), nf.createTemplate)
	return r
}

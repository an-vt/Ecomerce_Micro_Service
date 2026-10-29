package http_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	transport "github.com/an-vt/ecom/api-gateway/internal/transport/http"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
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

// ---- stub auth / rbac services (real gRPC servers over an in-memory connection) -----------------------------

type authStub struct {
	authv1.UnimplementedAuthServiceServer
	mu     sync.Mutex
	calls  map[string]int
	lastMD metadata.MD

	validate    func(*authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error)
	signUp      func(*authv1.SignUpShopRequest) (*authv1.AuthResponse, error)
	login       func(*authv1.LoginRequest) (*authv1.AuthResponse, error)
	refresh     func(*authv1.RefreshRequest) (*authv1.AuthResponse, error)
	logout      func(*authv1.LogoutRequest) error
	requestMail func(*authv1.RequestEmailVerificationRequest) error
	verify      func(*authv1.VerifyEmailRequest) (*authv1.AuthResponse, error)
	setPassword func(*authv1.SetPasswordRequest) error
	getAccount  func(*authv1.GetAccountRequest) (*authv1.Account, error)
}

func (s *authStub) enter(ctx context.Context, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[name]++
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
}

func (s *authStub) count(name string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls[name] }
func (s *authStub) md(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.lastMD.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func (s *authStub) ValidateApiKey(ctx context.Context, in *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
	s.enter(ctx, "ValidateApiKey")
	if s.validate != nil {
		r, err := s.validate(in)
		return r, grpcx.ToStatus(err)
	}
	return &authv1.ValidateApiKeyResponse{Valid: in.GetKey() == "good-key", Permissions: []string{"0000"}}, nil
}

func (s *authStub) SignUpShop(ctx context.Context, in *authv1.SignUpShopRequest) (*authv1.AuthResponse, error) {
	s.enter(ctx, "SignUpShop")
	if s.signUp == nil {
		return authResponse("shop-1", authv1.AccountType_ACCOUNT_TYPE_SHOP), nil
	}
	r, err := s.signUp(in)
	return r, grpcx.ToStatus(err)
}

func (s *authStub) Login(ctx context.Context, in *authv1.LoginRequest) (*authv1.AuthResponse, error) {
	s.enter(ctx, "Login")
	if s.login == nil {
		return authResponse("acc-1", in.GetType()), nil
	}
	r, err := s.login(in)
	return r, grpcx.ToStatus(err)
}

func (s *authStub) Refresh(ctx context.Context, in *authv1.RefreshRequest) (*authv1.AuthResponse, error) {
	s.enter(ctx, "Refresh")
	if s.refresh == nil {
		return authResponse("acc-1", authv1.AccountType_ACCOUNT_TYPE_SHOP), nil
	}
	r, err := s.refresh(in)
	return r, grpcx.ToStatus(err)
}

func (s *authStub) Logout(ctx context.Context, in *authv1.LogoutRequest) (*emptypb.Empty, error) {
	s.enter(ctx, "Logout")
	if s.logout != nil {
		return &emptypb.Empty{}, grpcx.ToStatus(s.logout(in))
	}
	return &emptypb.Empty{}, nil
}

func (s *authStub) RequestEmailVerification(ctx context.Context, in *authv1.RequestEmailVerificationRequest) (*emptypb.Empty, error) {
	s.enter(ctx, "RequestEmailVerification")
	if s.requestMail != nil {
		return &emptypb.Empty{}, grpcx.ToStatus(s.requestMail(in))
	}
	return &emptypb.Empty{}, nil
}

func (s *authStub) VerifyEmail(ctx context.Context, in *authv1.VerifyEmailRequest) (*authv1.AuthResponse, error) {
	s.enter(ctx, "VerifyEmail")
	if s.verify == nil {
		return authResponse("user-1", authv1.AccountType_ACCOUNT_TYPE_USER), nil
	}
	r, err := s.verify(in)
	return r, grpcx.ToStatus(err)
}

func (s *authStub) SetPassword(ctx context.Context, in *authv1.SetPasswordRequest) (*emptypb.Empty, error) {
	s.enter(ctx, "SetPassword")
	if s.setPassword != nil {
		return &emptypb.Empty{}, grpcx.ToStatus(s.setPassword(in))
	}
	return &emptypb.Empty{}, nil
}

func (s *authStub) GetAccount(ctx context.Context, in *authv1.GetAccountRequest) (*authv1.Account, error) {
	s.enter(ctx, "GetAccount")
	if s.getAccount != nil {
		r, err := s.getAccount(in)
		return r, grpcx.ToStatus(err)
	}
	return &authv1.Account{Id: "acc-1", Email: "a@b.co", Type: authv1.AccountType_ACCOUNT_TYPE_USER}, nil
}

func authResponse(id string, typ authv1.AccountType) *authv1.AuthResponse {
	return &authv1.AuthResponse{
		Account: &authv1.Account{Id: id, Type: typ, Email: "a@b.co", Name: "A", Status: "active", Roles: []string{"shop"}},
		Tokens:  &authv1.Tokens{AccessToken: "access-" + id, RefreshToken: "refresh-" + id, ExpiresInSeconds: 900, SessionId: "sid-" + id},
	}
}

type catalogStub struct {
	catalogv1.UnimplementedCatalogServiceServer
	mu     sync.Mutex
	last   metadata.MD
	list   func() (*catalogv1.ListProductsResponse, error)
	get    func(*catalogv1.ProductIdRequest) (*catalogv1.Product, error)
	create func(*catalogv1.CreateProductRequest) (*catalogv1.Product, error)
	update func(*catalogv1.UpdateProductRequest) (*catalogv1.Product, error)
}

func (s *catalogStub) enter(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, _ = metadata.FromIncomingContext(ctx)
}

func (s *catalogStub) ListProducts(ctx context.Context, in *catalogv1.ListProductsRequest) (*catalogv1.ListProductsResponse, error) {
	s.enter(ctx)
	if s.list != nil {
		r, err := s.list()
		return r, grpcx.ToStatus(err)
	}
	return &catalogv1.ListProductsResponse{Products: []*catalogv1.Product{{Id: "p1", Name: "Shirt", AttributesJson: `{"size":"M"}`}}, Total: 1, Page: in.GetPage(), Limit: in.GetLimit()}, nil
}

func (s *catalogStub) GetProduct(ctx context.Context, in *catalogv1.ProductIdRequest) (*catalogv1.Product, error) {
	s.enter(ctx)
	if s.get != nil {
		r, err := s.get(in)
		return r, grpcx.ToStatus(err)
	}
	return &catalogv1.Product{Id: in.GetProductId(), Name: "Shirt", Price: 100, AttributesJson: `{"size":"M"}`}, nil
}

func (s *catalogStub) CreateProduct(ctx context.Context, in *catalogv1.CreateProductRequest) (*catalogv1.Product, error) {
	s.enter(ctx)
	if s.create != nil {
		r, err := s.create(in)
		return r, grpcx.ToStatus(err)
	}
	return &catalogv1.Product{Id: "p-new", Name: in.GetName(), Type: in.GetType(), IsDraft: true, AttributesJson: in.GetAttributesJson()}, nil
}

func (s *catalogStub) UpdateProduct(ctx context.Context, in *catalogv1.UpdateProductRequest) (*catalogv1.Product, error) {
	s.enter(ctx)
	if s.update != nil {
		r, err := s.update(in)
		return r, grpcx.ToStatus(err)
	}
	return &catalogv1.Product{Id: in.GetProductId(), AttributesJson: in.GetAttributesJson()}, nil
}

func (s *catalogStub) PublishProduct(ctx context.Context, in *catalogv1.ProductIdRequest) (*catalogv1.Product, error) {
	s.enter(ctx)
	return &catalogv1.Product{Id: in.GetProductId(), IsPublished: true}, nil
}

func (s *catalogStub) UnpublishProduct(ctx context.Context, in *catalogv1.ProductIdRequest) (*catalogv1.Product, error) {
	s.enter(ctx)
	return &catalogv1.Product{Id: in.GetProductId()}, nil
}

func (s *catalogStub) SearchProducts(ctx context.Context, in *catalogv1.SearchProductsRequest) (*catalogv1.ListProductsResponse, error) {
	s.enter(ctx)
	return &catalogv1.ListProductsResponse{Products: []*catalogv1.Product{{Id: "found-" + in.GetKeyword()}}, Total: 1}, nil
}

func (s *catalogStub) ListShopProducts(ctx context.Context, in *catalogv1.ListShopProductsRequest) (*catalogv1.ListProductsResponse, error) {
	s.enter(ctx)
	id := "draft"
	if in.GetPublished() {
		id = "published"
	}
	return &catalogv1.ListProductsResponse{Products: []*catalogv1.Product{{Id: id}}, Total: 1}, nil
}

func (s *catalogStub) CreateSpu(ctx context.Context, in *catalogv1.CreateSpuRequest) (*catalogv1.SpuResponse, error) {
	s.enter(ctx)
	r := &catalogv1.SpuResponse{Spu: &catalogv1.Spu{Id: "spu-1", Name: in.GetName(), Categories: in.GetCategories(), Variations: in.GetVariations()}}
	for i, k := range in.GetSkus() {
		r.Skus = append(r.Skus, &catalogv1.Sku{Id: "spu-1." + string(rune('1'+i)), SpuId: "spu-1", TierIdx: k.GetTierIdx(), Price: k.GetPrice()})
	}
	return r, nil
}

func (s *catalogStub) GetSpu(ctx context.Context, in *catalogv1.GetSpuRequest) (*catalogv1.SpuResponse, error) {
	s.enter(ctx)
	return &catalogv1.SpuResponse{Spu: &catalogv1.Spu{Id: in.GetSpuId()}, Skus: []*catalogv1.Sku{{Id: in.GetSpuId() + ".1", SpuId: in.GetSpuId()}}}, nil
}

func (s *catalogStub) GetSku(ctx context.Context, in *catalogv1.GetSkuRequest) (*catalogv1.Sku, error) {
	s.enter(ctx)
	return &catalogv1.Sku{Id: in.GetSkuId(), SpuId: in.GetSpuId(), Price: 55}, nil
}

func (s *catalogStub) ListComments(ctx context.Context, in *catalogv1.ListCommentsRequest) (*catalogv1.ListCommentsResponse, error) {
	s.enter(ctx)
	return &catalogv1.ListCommentsResponse{Comments: []*catalogv1.Comment{{Id: "c1", ProductId: in.GetProductId(), ParentId: in.GetParentId(), Content: "hi"}}}, nil
}

func (s *catalogStub) CreateComment(ctx context.Context, in *catalogv1.CreateCommentRequest) (*catalogv1.Comment, error) {
	s.enter(ctx)
	return &catalogv1.Comment{Id: "c-new", ProductId: in.GetProductId(), Content: in.GetContent(), ParentId: in.GetParentCommentId()}, nil
}

func (s *catalogStub) DeleteComment(ctx context.Context, in *catalogv1.DeleteCommentRequest) (*emptypb.Empty, error) {
	s.enter(ctx)
	return &emptypb.Empty{}, nil
}

type inventoryStub struct {
	inventoryv1.UnimplementedInventoryServiceServer
	mu     sync.Mutex
	last   metadata.MD
	addErr error
}

func (s *inventoryStub) enter(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, _ = metadata.FromIncomingContext(ctx)
}

func (s *inventoryStub) AddStock(ctx context.Context, in *inventoryv1.AddStockRequest) (*inventoryv1.Stock, error) {
	s.enter(ctx)
	if s.addErr != nil {
		return nil, grpcx.ToStatus(s.addErr)
	}
	return &inventoryv1.Stock{ProductId: in.GetProductId(), Stock: 10 + in.GetStock(), Location: in.GetLocation()}, nil
}

func (s *inventoryStub) GetStock(ctx context.Context, in *inventoryv1.GetStockRequest) (*inventoryv1.Stock, error) {
	s.enter(ctx)
	return &inventoryv1.Stock{ProductId: in.GetProductId(), Stock: 7, Reserved: 2}, nil
}

type cartStub struct {
	cartv1.UnimplementedCartServiceServer
	mu        sync.Mutex
	last      metadata.MD
	addReq    *cartv1.AddToCartRequest
	updateReq *cartv1.UpdateCartRequest
	fail      error
}

func (s *cartStub) enter(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, _ = metadata.FromIncomingContext(ctx)
	return grpcx.ToStatus(s.fail)
}

func (s *cartStub) AddToCart(ctx context.Context, in *cartv1.AddToCartRequest) (*cartv1.Cart, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.addReq = in
	return &cartv1.Cart{Id: "c1", UserId: "u1", State: "active", CountProduct: 1, Total: 2000,
		Items: []*cartv1.CartItem{{ProductId: in.GetProductId(), Quantity: in.GetQuantity(), Price: 1000}}}, nil
}

func (s *cartStub) UpdateCart(ctx context.Context, in *cartv1.UpdateCartRequest) (*cartv1.Cart, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.updateReq = in
	return &cartv1.Cart{Id: "c1", CountProduct: int64(len(in.GetLines()))}, nil
}

func (s *cartStub) RemoveItem(ctx context.Context, in *cartv1.RemoveItemRequest) (*cartv1.Cart, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	return &cartv1.Cart{Id: "c1"}, nil
}

func (s *cartStub) GetCart(ctx context.Context, _ *cartv1.GetCartRequest) (*cartv1.Cart, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	return &cartv1.Cart{Id: "c1", UserId: "u1", State: "active"}, nil
}

type discountStub struct {
	discountv1.UnimplementedDiscountServiceServer
	mu        sync.Mutex
	last      metadata.MD
	createReq *discountv1.CreateDiscountRequest
	updateReq *discountv1.UpdateDiscountRequest
	listReq   *discountv1.ListShopDiscountsRequest
	codeReq   *discountv1.ListProductsForCodeRequest
	calcReq   *discountv1.CalculateDiscountRequest
	fail      error
}

func (s *discountStub) enter(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, _ = metadata.FromIncomingContext(ctx)
	return grpcx.ToStatus(s.fail)
}

func (s *discountStub) CreateDiscount(ctx context.Context, in *discountv1.CreateDiscountRequest) (*discountv1.Discount, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.createReq = in
	return &discountv1.Discount{Id: "d1", ShopId: "shop1", Code: in.GetCode(), Name: in.GetName(), Type: in.GetType(), Value: in.GetValue(), IsActive: true, AppliesTo: "all"}, nil
}

func (s *discountStub) UpdateDiscount(ctx context.Context, in *discountv1.UpdateDiscountRequest) (*discountv1.Discount, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.updateReq = in
	return &discountv1.Discount{Id: in.GetDiscountId(), Name: in.GetName(), ProductIds: in.GetProductIds()}, nil
}

func (s *discountStub) ListShopDiscounts(ctx context.Context, in *discountv1.ListShopDiscountsRequest) (*discountv1.ListShopDiscountsResponse, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.listReq = in
	return &discountv1.ListShopDiscountsResponse{Total: 1, Discounts: []*discountv1.Discount{{Id: "d1", Code: "SALE10", UsedCount: 2}}}, nil
}

func (s *discountStub) ListProductsForCode(ctx context.Context, in *discountv1.ListProductsForCodeRequest) (*discountv1.ListProductsForCodeResponse, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.codeReq = in
	return &discountv1.ListProductsForCodeResponse{Total: 1, Products: []*discountv1.ProductSummary{{Id: "p1", Name: "Shirt", Price: 1000, Thumb: "t"}}}, nil
}

func (s *discountStub) CalculateDiscount(ctx context.Context, in *discountv1.CalculateDiscountRequest) (*discountv1.CalculateDiscountResponse, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.calcReq = in
	return &discountv1.CalculateDiscountResponse{Subtotal: 200000, Discount: 20000, Total: 180000}, nil
}

type orderStub struct {
	orderv1.UnimplementedOrderServiceServer
	mu        sync.Mutex
	last      metadata.MD
	reviewReq *orderv1.ReviewCheckoutRequest
	placeReq  *orderv1.PlaceOrderRequest
	getReq    *orderv1.GetOrderRequest
	listReq   *orderv1.ListOrdersRequest
	fail      error
}

func (s *orderStub) enter(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, _ = metadata.FromIncomingContext(ctx)
	return grpcx.ToStatus(s.fail)
}

func (s *orderStub) ReviewCheckout(ctx context.Context, in *orderv1.ReviewCheckoutRequest) (*orderv1.ReviewCheckoutResponse, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.reviewReq = in
	return &orderv1.ReviewCheckoutResponse{TotalPrice: 250000, TotalDiscount: 20000, FeeShip: 5000, TotalCheckout: 235000,
		Shops: []*orderv1.ReviewShop{{ShopId: "s1", Subtotal: 200000, DiscountCode: "SALE10", Discount: 20000, Total: 180000,
			Items: []*orderv1.ReviewItem{{ProductId: "p1", Name: "Shirt", Price: 100000, Quantity: 2}}}, {ShopId: "s2", Subtotal: 50000, Total: 50000}}}, nil
}

func (s *orderStub) PlaceOrder(ctx context.Context, in *orderv1.PlaceOrderRequest) (*orderv1.PlaceOrderResponse, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.placeReq = in
	return &orderv1.PlaceOrderResponse{OrderId: "o1", Status: "PENDING"}, nil
}

func (s *orderStub) GetOrder(ctx context.Context, in *orderv1.GetOrderRequest) (*orderv1.Order, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.getReq = in
	return &orderv1.Order{Id: in.GetOrderId(), UserId: "u1", Status: "CANCELLED", CancelReason: "CARD_DECLINED", TotalCheckout: 235000, PaymentMethod: "card",
		Shipping: &orderv1.Shipping{Address: "1 Main St"}, Items: []*orderv1.OrderItem{{ProductId: "p1", ShopId: "s1", Name: "Shirt", Price: 1, Quantity: 2}},
		Discounts: []*orderv1.DiscountRef{{ShopId: "s1", Code: "SALE10"}}}, nil
}

func (s *orderStub) ListOrders(ctx context.Context, in *orderv1.ListOrdersRequest) (*orderv1.ListOrdersResponse, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.listReq = in
	return &orderv1.ListOrdersResponse{Total: 1, Orders: []*orderv1.Order{{Id: "o1", Status: "CONFIRMED"}}}, nil
}

type notificationStub struct {
	notificationv1.UnimplementedNotificationServiceServer
	mu        sync.Mutex
	last      metadata.MD
	listReq   *notificationv1.ListNotificationsRequest
	createReq *notificationv1.CreateTemplateRequest
	fail      error
}

func (s *notificationStub) enter(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, _ = metadata.FromIncomingContext(ctx)
	return grpcx.ToStatus(s.fail)
}

func (s *notificationStub) ListNotifications(ctx context.Context, in *notificationv1.ListNotificationsRequest) (*notificationv1.ListNotificationsResponse, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.listReq = in
	return &notificationv1.ListNotificationsResponse{Total: 2, Notifications: []*notificationv1.Notification{
		{Id: "n1", Type: "ORDER-001", SenderId: "order-service", ReceiverId: "u1", Content: "Your order o1 was confirmed", OptionsJson: `{"orderId":"o1","total":5000}`, CreatedAt: "2026-10-30T12:00:00Z"},
		{Id: "n2", Type: "SHOP-001", ReceiverId: "*", Content: "A shop just added a new product: Shirt"}}}, nil
}

func (s *notificationStub) CreateTemplate(ctx context.Context, in *notificationv1.CreateTemplateRequest) (*notificationv1.Template, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	s.createReq = in
	return &notificationv1.Template{Id: "t1", Name: in.GetName(), Html: in.GetHtml(), Status: "active", CreatedAt: "2026-10-30T12:00:00Z"}, nil
}

type rbacStub struct {
	authv1.UnimplementedRbacServiceServer
	grants     func() ([]*authv1.RoleGrant, error)
	createRole func(*authv1.CreateRoleRequest) (*authv1.Role, error)
}

func (s *rbacStub) GetGrants(context.Context, *emptypb.Empty) (*authv1.GetGrantsResponse, error) {
	if s.grants == nil {
		return &authv1.GetGrantsResponse{}, nil
	}
	g, err := s.grants()
	return &authv1.GetGrantsResponse{Grants: g}, grpcx.ToStatus(err)
}

func (s *rbacStub) CreateRole(_ context.Context, in *authv1.CreateRoleRequest) (*authv1.Role, error) {
	if s.createRole != nil {
		r, err := s.createRole(in)
		return r, grpcx.ToStatus(err)
	}
	return &authv1.Role{Id: "r1", Name: in.GetName(), Status: "active", Grants: in.GetGrants()}, nil
}

func (s *rbacStub) ListRoles(context.Context, *emptypb.Empty) (*authv1.ListRolesResponse, error) {
	return &authv1.ListRolesResponse{Roles: []*authv1.Role{{Id: "r1", Name: "shop"}}}, nil
}

func (s *rbacStub) CreateResource(_ context.Context, in *authv1.CreateResourceRequest) (*authv1.Resource, error) {
	return &authv1.Resource{Id: "res1", Name: in.GetName()}, nil
}

func (s *rbacStub) ListResources(context.Context, *emptypb.Empty) (*authv1.ListResourcesResponse, error) {
	return &authv1.ListResourcesResponse{Resources: []*authv1.Resource{{Id: "res1", Name: "profile"}}}, nil
}

// ---- the gateway under test ------------------------------------------------------------------------------------

var testKey *rsa.PrivateKey
var keyOnce sync.Once

func signerKey() *rsa.PrivateKey {
	keyOnce.Do(func() { testKey, _ = rsa.GenerateKey(rand.Reader, 2048) })
	return testKey
}

type gw struct {
	t            *testing.T
	engine       *gin.Engine
	auth         *authStub
	rbac         *rbacStub
	catalog      *catalogStub
	inventory    *inventoryStub
	cart         *cartStub
	discount     *discountStub
	order        *orderStub
	notification *notificationStub
	mr           *miniredis.Miniredis
	signer       *jwtx.Signer
	clk          *clock.Fixed
	metrics      *metrics.Metrics
	breaker      *breaker.Registry
	stop         func()
}

type gwOpts struct {
	rateLimit, authRateLimit int
	corsOrigins              []string
	bodyLimit                int64
	breakerMin               uint32
	breakerReset             time.Duration
	grantsTTL                time.Duration
}

func newGateway(t *testing.T, mut ...func(*gwOpts)) *gw {
	t.Helper()
	o := gwOpts{rateLimit: 1000, authRateLimit: 1000, bodyLimit: 100 * 1024, breakerMin: 10, breakerReset: 30 * time.Second, grantsTTL: time.Minute}
	for _, m := range mut {
		m(&o)
	}
	g := &gw{t: t, auth: &authStub{}, rbac: &rbacStub{}, catalog: &catalogStub{}, inventory: &inventoryStub{}, cart: &cartStub{}, discount: &discountStub{}, order: &orderStub{}, notification: &notificationStub{}, clk: clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)), metrics: metrics.New()}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	authv1.RegisterAuthServiceServer(srv, g.auth)
	authv1.RegisterRbacServiceServer(srv, g.rbac)
	catalogv1.RegisterCatalogServiceServer(srv, g.catalog)
	inventoryv1.RegisterInventoryServiceServer(srv, g.inventory)
	cartv1.RegisterCartServiceServer(srv, g.cart)
	discountv1.RegisterDiscountServiceServer(srv, g.discount)
	orderv1.RegisterOrderServiceServer(srv, g.order)
	notificationv1.RegisterNotificationServiceServer(srv, g.notification)
	go func() { _ = srv.Serve(lis) }()
	g.stop = srv.Stop
	t.Cleanup(srv.Stop)

	g.breaker = breaker.NewRegistry(grpcx.BreakerSettings(breaker.Settings{MinRequests: o.breakerMin, ErrorRatio: 0.5, ResetTimeout: o.breakerReset, Observer: g.metrics.BreakerObserver()}))
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithChainUnaryInterceptor(grpcx.ClientInterceptor(grpcx.ClientConfig{Name: "auth", Timeout: 2 * time.Second, Breakers: g.breaker, Metrics: g.metrics, Retries: 1})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	authClient, rbacClient := authv1.NewAuthServiceClient(conn), authv1.NewRbacServiceClient(conn)
	catalogClient := catalogv1.NewCatalogServiceClient(conn)
	inventoryClient := inventoryv1.NewInventoryServiceClient(conn)
	cartClient := cartv1.NewCartServiceClient(conn)
	discountClient := discountv1.NewDiscountServiceClient(conn)
	orderClient := orderv1.NewOrderServiceClient(conn)
	notificationClient := notificationv1.NewNotificationServiceClient(conn)

	g.mr = miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: g.mr.Addr(), ReadTimeout: 300 * time.Millisecond, DialTimeout: 300 * time.Millisecond,
		DialerRetries: 1, DialerRetryTimeout: 20 * time.Millisecond, MaxRetries: 1, PoolTimeout: 300 * time.Millisecond})
	t.Cleanup(func() { _ = rdb.Close() })

	g.signer = jwtx.NewSigner(signerKey(), "ecom-auth", "ecom", 15*time.Minute, g.clk)
	verifier := jwtx.NewVerifier("ecom-auth", "ecom", g.clk, nil)
	require.NoError(t, verifier.SetKeys(g.signer.JWKS()))

	grants := transport.NewGrantsCache(func(ctx context.Context) ([]rbac.Grant, error) {
		r, err := rbacClient.GetGrants(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, err
		}
		out := make([]rbac.Grant, 0, len(r.GetGrants()))
		for _, x := range r.GetGrants() {
			out = append(out, rbac.Grant{Role: x.GetRole(), Resource: x.GetResource(), Actions: x.GetActions(), Attributes: x.GetAttributes()})
		}
		return out, nil
	}, o.grantsTTL, g.clk)

	g.rbac.grants = func() ([]*authv1.RoleGrant, error) {
		return []*authv1.RoleGrant{
			{Role: "user", Resource: "profile", Actions: []string{"readOwn", "updateOwn"}},
			{Role: "shop", Resource: "profile", Actions: []string{"readOwn"}},
			{Role: "admin", Resource: "profile", Actions: []string{"readAny", "readOwn"}},
			{Role: "admin", Resource: "rbac", Actions: []string{"createAny", "readAny"}},
			{Role: "admin", Resource: "template", Actions: []string{"createAny"}},
		}, nil
	}

	g.engine = transport.NewRouter(transport.Options{
		Upstreams: transport.Upstreams{Auth: authClient, Rbac: rbacClient, Catalog: catalogClient, Inventory: inventoryClient, Cart: cartClient, Discount: discountClient, Order: orderClient, Notification: notificationClient},
		Verifier:  verifier, Denylist: redisx.NewDenylist(rdb, "auth:revoked:"), Limiter: redisx.NewLimiter(rdb, "rl:"),
		APICache: redisx.NewCache(rdb, "apikey:"), Grants: grants, Metrics: g.metrics, IDs: nil,
		CORSOrigins: o.corsOrigins, CookieSecure: false, CookieTTL: 7 * 24 * time.Hour,
		RateLimit: o.rateLimit, AuthRateLimit: o.authRateLimit, BodyLimit: o.bodyLimit,
		APIKeyTTL: time.Minute, APIKeyNegTTL: 10 * time.Second,
	})
	return g
}

// token mints an access token like auth-service would.
func (g *gw) token(accountID, typ string, roles ...string) string {
	g.t.Helper()
	tok, _, err := g.signer.Sign(accountID, typ, "a@b.co", roles, "sid-"+accountID)
	require.NoError(g.t, err)
	return tok
}

type req struct {
	method, path, body string
	headers            map[string]string
	cookies            []*stdhttp.Cookie
}

type resp struct {
	*httptest.ResponseRecorder
}

func (g *gw) do(r req) resp {
	g.t.Helper()
	var body io.Reader
	if r.body != "" {
		body = bytes.NewBufferString(r.body)
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	if r.body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	for _, c := range r.cookies {
		hr.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	g.engine.ServeHTTP(rec, hr)
	return resp{rec}
}

// call is a request with the public API key already set.
func (g *gw) call(method, path, body string, hdr ...string) resp {
	g.t.Helper()
	h := map[string]string{"x-api-key": "good-key"}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	return g.do(req{method: method, path: path, body: body, headers: h})
}

// as is call with a bearer token.
func (g *gw) as(token, method, path, body string) resp {
	g.t.Helper()
	return g.call(method, path, body, "Authorization", "Bearer "+token)
}

func (r resp) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &m)
	return m
}

func (r resp) errCode() string {
	m := r.json()
	s, _ := m["error"].(string)
	return s
}

func (r resp) cookie(name string) *stdhttp.Cookie {
	for _, c := range r.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

var _ = errs.KindOf

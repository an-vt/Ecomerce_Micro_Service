package grpc_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/catalog-service/internal/fake"
	transport "github.com/an-vt/ecom/catalog-service/internal/transport/grpc"
	"github.com/an-vt/ecom/catalog-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/metrics"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
)

func start(t *testing.T) (catalogv1.CatalogServiceClient, *fake.World) {
	t.Helper()
	w := fake.NewWorld()
	uc := usecase.NewSet(&usecase.Deps{Products: w.Products(), Spus: w.Spus(), Comments: w.Comments(), Shops: w.Shops(),
		Types: domain.DefaultTypes(), Tx: w, Outbox: w, SkuCache: fake.NewCache(),
		Clock: clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)), IDs: &idgen.Sequence{Prefix: "id-"}})
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer("catalog-service", metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	catalogv1.RegisterCatalogServiceServer(srv, transport.NewServer(uc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return grpcx.FromStatus(inv(ctx, m, req, reply, cc, o...))
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return catalogv1.NewCatalogServiceClient(conn), w
}

func as(id, typ, roles string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), grpcx.MDAccountID, id, grpcx.MDAccountType, typ, grpcx.MDRoles, roles)
}

var bg = context.Background()

func shirt(name string) *catalogv1.CreateProductRequest {
	return &catalogv1.CreateProductRequest{Type: "Clothing", Name: name, Description: "cotton", Price: 150000, Quantity: 5,
		AttributesJson: `{"brand":"Acme","size":"M","material":"cotton","extra":{"fit":"slim"}}`}
}

func TestProductLifecycleOverGRPC(t *testing.T) {
	c, w := start(t)
	shop := as("shop1", "SHOP", "shop")

	p, err := c.CreateProduct(shop, shirt("Blue shirt"))
	require.NoError(t, err)
	assert.True(t, p.GetIsDraft())
	assert.Equal(t, "blue-shirt", p.GetSlug())
	assert.JSONEq(t, `{"brand":"Acme","size":"M","material":"cotton","extra":{"fit":"slim"}}`, p.GetAttributesJson())
	assert.Equal(t, []string{"catalog.product.created"}, w.OutboxTypes())

	_, err = c.GetProduct(bg, &catalogv1.ProductIdRequest{ProductId: p.GetId()})
	assert.Equal(t, domain.CodeProductNotFound, errs.CodeOf(err), "drafts are not public")

	name, qty := "Blue shirt v2", int64(9)
	up, err := c.UpdateProduct(shop, &catalogv1.UpdateProductRequest{ProductId: p.GetId(), Name: &name, Quantity: &qty, AttributesJson: strPtr(`{"size":"L"}`)})
	require.NoError(t, err)
	assert.Equal(t, int64(9), up.GetQuantity())
	assert.JSONEq(t, `{"brand":"Acme","size":"L","material":"cotton","extra":{"fit":"slim"}}`, up.GetAttributesJson())

	pub, err := c.PublishProduct(shop, &catalogv1.ProductIdRequest{ProductId: p.GetId()})
	require.NoError(t, err)
	assert.True(t, pub.GetIsPublished())
	got, err := c.GetProduct(bg, &catalogv1.ProductIdRequest{ProductId: p.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "Blue shirt v2", got.GetName())

	list, err := c.ListProducts(bg, &catalogv1.ListProductsRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), list.GetTotal())
	assert.Equal(t, int32(50), list.GetLimit())
	found, err := c.SearchProducts(bg, &catalogv1.SearchProductsRequest{Keyword: "blue"})
	require.NoError(t, err)
	assert.Len(t, found.GetProducts(), 1)
	mine, err := c.ListShopProducts(shop, &catalogv1.ListShopProductsRequest{Published: true})
	require.NoError(t, err)
	assert.Len(t, mine.GetProducts(), 1)

	by, err := c.GetProductsByIds(bg, &catalogv1.GetProductsByIdsRequest{Ids: []string{p.GetId(), "ghost"}})
	require.NoError(t, err)
	assert.Len(t, by.GetProducts(), 1)
	ids, err := c.ListProductIdsByShop(bg, &catalogv1.ListProductIdsByShopRequest{ShopId: "shop1"})
	require.NoError(t, err)
	assert.Equal(t, []string{p.GetId()}, ids.GetIds())

	un, err := c.UnpublishProduct(shop, &catalogv1.ProductIdRequest{ProductId: p.GetId()})
	require.NoError(t, err)
	assert.False(t, un.GetIsPublished())
}

func strPtr(s string) *string { return &s }

func TestProductErrorsKeepKindAndCode(t *testing.T) {
	c, _ := start(t)
	shop := as("shop1", "SHOP", "shop")

	_, err := c.CreateProduct(bg, shirt("x"))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = c.CreateProduct(as("u1", "USER", "user"), shirt("x"))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))

	bad := shirt("y")
	bad.AttributesJson = `{"brand":"Acme"}`
	_, err = c.CreateProduct(shop, bad)
	assert.Equal(t, domain.CodeInvalidAttribute, errs.CodeOf(err))
	bad.AttributesJson = `{not json`
	_, err = c.CreateProduct(shop, bad)
	assert.Equal(t, domain.CodeInvalidAttribute, errs.CodeOf(err))
	_, err = c.UpdateProduct(shop, &catalogv1.UpdateProductRequest{ProductId: "p", AttributesJson: strPtr(`[1]`)})
	assert.Equal(t, domain.CodeInvalidAttribute, errs.CodeOf(err))

	p, _ := c.CreateProduct(shop, shirt("z"))
	_, err = c.UpdateProduct(as("shop2", "SHOP", "shop"), &catalogv1.UpdateProductRequest{ProductId: p.GetId()})
	assert.Equal(t, domain.CodeNotOwner, errs.CodeOf(err))
	_, err = c.SearchProducts(bg, &catalogv1.SearchProductsRequest{Keyword: " "})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = c.PublishProduct(as("shop2", "SHOP", "shop"), &catalogv1.ProductIdRequest{ProductId: p.GetId()})
	assert.Equal(t, domain.CodeNotOwner, errs.CodeOf(err))
}

func TestSpuSkuOverGRPC(t *testing.T) {
	c, _ := start(t)
	shop := as("shop1", "SHOP", "shop")
	r, err := c.CreateSpu(shop, &catalogv1.CreateSpuRequest{Name: "T-Shirt", Price: 100, Categories: []string{"clothes"}, Quantity: 10,
		AttributesJson: `{"brand":"Acme"}`,
		Variations:     []*catalogv1.Variation{{Name: "color", Options: []string{"red", "blue"}}},
		Skus:           []*catalogv1.SkuInput{{TierIdx: []int32{0}, Price: 100, Stock: 5, Sort: 2}, {TierIdx: []int32{1}, Price: 110, Stock: 2, Sort: 1, IsDefault: true}}})
	require.NoError(t, err)
	require.Len(t, r.GetSkus(), 2)
	assert.Equal(t, "t-shirt", r.GetSpu().GetSlug())

	got, err := c.GetSpu(bg, &catalogv1.GetSpuRequest{SpuId: r.GetSpu().GetId()})
	require.NoError(t, err)
	assert.Equal(t, int32(1), got.GetSkus()[0].GetSort(), "skus come ordered by sort")
	assert.Equal(t, []int32{1}, got.GetSkus()[0].GetTierIdx())
	assert.Equal(t, "color", got.GetSpu().GetVariations()[0].GetName())

	sku, err := c.GetSku(bg, &catalogv1.GetSkuRequest{SkuId: got.GetSkus()[0].GetId(), SpuId: r.GetSpu().GetId()})
	require.NoError(t, err)
	assert.Equal(t, int64(110), sku.GetPrice())
	_, err = c.GetSku(bg, &catalogv1.GetSkuRequest{SkuId: "ghost", SpuId: r.GetSpu().GetId()})
	assert.Equal(t, domain.CodeSkuNotFound, errs.CodeOf(err))
	_, err = c.GetSpu(bg, &catalogv1.GetSpuRequest{SpuId: "ghost"})
	assert.Equal(t, domain.CodeSpuNotFound, errs.CodeOf(err))
	_, err = c.CreateSpu(shop, &catalogv1.CreateSpuRequest{Name: "Bad", Categories: []string{"c"}, AttributesJson: `{`})
	assert.Equal(t, domain.CodeInvalidAttribute, errs.CodeOf(err))
}

func TestCommentsOverGRPC(t *testing.T) {
	c, _ := start(t)
	shop := as("shop1", "SHOP", "shop")
	p, _ := c.CreateProduct(shop, shirt("Shirt"))
	user := as("u1", "USER", "user")

	root, err := c.CreateComment(user, &catalogv1.CreateCommentRequest{ProductId: p.GetId(), Content: "great"})
	require.NoError(t, err)
	assert.Equal(t, "u1", root.GetUserId())
	reply, err := c.CreateComment(as("u2", "USER", "user"), &catalogv1.CreateCommentRequest{ProductId: p.GetId(), Content: "agreed", ParentCommentId: root.GetId()})
	require.NoError(t, err)
	assert.Equal(t, root.GetId(), reply.GetParentId())

	kids, err := c.ListComments(bg, &catalogv1.ListCommentsRequest{ProductId: p.GetId(), ParentId: root.GetId()})
	require.NoError(t, err)
	assert.Len(t, kids.GetComments(), 1)

	_, err = c.DeleteComment(as("u3", "USER", "user"), &catalogv1.DeleteCommentRequest{CommentId: root.GetId(), ProductId: p.GetId()})
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
	_, err = c.DeleteComment(user, &catalogv1.DeleteCommentRequest{CommentId: root.GetId(), ProductId: p.GetId()})
	require.NoError(t, err)
	top, _ := c.ListComments(bg, &catalogv1.ListCommentsRequest{ProductId: p.GetId()})
	assert.Empty(t, top.GetComments())
	_, err = c.CreateComment(bg, &catalogv1.CreateCommentRequest{ProductId: p.GetId(), Content: "x"})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
}

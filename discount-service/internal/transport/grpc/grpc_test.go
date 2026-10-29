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

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/discount-service/internal/fake"
	transport "github.com/an-vt/ecom/discount-service/internal/transport/grpc"
	"github.com/an-vt/ecom/discount-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/metrics"
	discountv1 "github.com/an-vt/ecom/proto/gen/go/discount/v1"
)

var now = time.Now().UTC().Truncate(time.Second)

func start(t *testing.T) discountv1.DiscountServiceClient {
	t.Helper()
	w := fake.NewWorld()
	cat := &fake.Catalog{Shops: map[string][]string{"shop1": {"p1", "p2"}},
		Products_: map[string]domain.ProductSummary{"p1": {ID: "p1", ShopID: "shop1", Name: "Shirt", Price: 1000, Thumb: "t", IsPublished: true}}}
	uc := usecase.NewSet(&usecase.Deps{Discounts: w.Discounts(), Usages: w.Usages(), Catalog: cat, Tx: w, Outbox: w, Clock: clock.NewFixed(now), IDs: idgen.UUID{}})
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer("discount-service", metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	discountv1.RegisterDiscountServiceServer(srv, transport.NewServer(uc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return grpcx.FromStatus(inv(ctx, m, req, reply, cc, o...))
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return discountv1.NewDiscountServiceClient(conn)
}

func as(id, typ string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), grpcx.MDAccountID, id, grpcx.MDAccountType, typ, grpcx.MDRoles, "x")
}

func TestDiscountOverGRPC(t *testing.T) {
	c := start(t)
	shop, buyer := as("shop1", "SHOP"), as("alice", "USER")

	created, err := c.CreateDiscount(shop, &discountv1.CreateDiscountRequest{Code: "sale10", Name: "Ten", Type: "percent", Value: 10,
		StartDate: now.Add(-time.Hour).Format(time.RFC3339), EndDate: now.Add(24 * time.Hour).Format(time.RFC3339), MaxUses: 5, MaxUsesPerUser: 1,
		AppliesTo: "specific", ProductIds: []string{"p1"}})
	require.NoError(t, err)
	assert.Equal(t, "SALE10", created.GetCode())
	assert.Equal(t, "shop1", created.GetShopId())
	assert.Equal(t, []string{"p1"}, created.GetProductIds())
	assert.Equal(t, now.Add(24*time.Hour).Format(time.RFC3339), created.GetEndDate())

	_, err = c.CreateDiscount(shop, &discountv1.CreateDiscountRequest{Code: "x", StartDate: "yesterday", EndDate: now.Format(time.RFC3339)})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = c.CreateDiscount(shop, &discountv1.CreateDiscountRequest{Code: "x", StartDate: now.Format(time.RFC3339), EndDate: "tomorrow"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))

	name, later := "Renamed", now.Add(48*time.Hour).Format(time.RFC3339)
	upd, err := c.UpdateDiscount(shop, &discountv1.UpdateDiscountRequest{DiscountId: created.GetId(), Name: &name, EndDate: &later})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", upd.GetName())
	assert.Equal(t, later, upd.GetEndDate())
	bad := "soon"
	_, err = c.UpdateDiscount(shop, &discountv1.UpdateDiscountRequest{DiscountId: created.GetId(), EndDate: &bad})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = c.UpdateDiscount(as("shop2", "SHOP"), &discountv1.UpdateDiscountRequest{DiscountId: created.GetId(), Name: &name})
	assert.Equal(t, domain.CodeNotOwner, errs.CodeOf(err))

	list, err := c.ListShopDiscounts(shop, &discountv1.ListShopDiscountsRequest{Page: 1, Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 1, list.GetTotal())
	require.Len(t, list.GetDiscounts(), 1)

	products, err := c.ListProductsForCode(context.Background(), &discountv1.ListProductsForCodeRequest{Code: "SALE10", ShopId: "shop1", Page: 1, Limit: 10})
	require.NoError(t, err)
	require.Len(t, products.GetProducts(), 1)
	assert.Equal(t, "Shirt", products.GetProducts()[0].GetName())
	assert.Equal(t, "t", products.GetProducts()[0].GetThumb())

	calc, err := c.CalculateDiscount(buyer, &discountv1.CalculateDiscountRequest{Code: "sale10", ShopId: "shop1",
		Lines: []*discountv1.OrderLine{{ProductId: "p1", Price: 100000, Quantity: 2}, {ProductId: "other", Price: 5000, Quantity: 1}}})
	require.NoError(t, err)
	assert.EqualValues(t, 20000, calc.GetDiscount(), "10% of the covered product only")
	assert.EqualValues(t, 205000, calc.GetSubtotal())
	assert.EqualValues(t, 185000, calc.GetTotal())
	_, err = c.CalculateDiscount(shop, &discountv1.CalculateDiscountRequest{Code: "sale10", ShopId: "shop1"})
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
}

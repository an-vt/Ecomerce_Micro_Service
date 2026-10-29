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

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/order-service/internal/fake"
	transport "github.com/an-vt/ecom/order-service/internal/transport/grpc"
	"github.com/an-vt/ecom/order-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/metrics"
	orderv1 "github.com/an-vt/ecom/proto/gen/go/order/v1"
)

func start(t *testing.T) (orderv1.OrderServiceClient, *fake.World) {
	t.Helper()
	w := fake.NewWorld()
	cat := &fake.Catalog{Products_: map[string]domain.Product{
		"p1": {ID: "p1", ShopID: "s1", Name: "Shirt", Price: 100000, IsPublished: true}}}
	uc := usecase.NewSet(&usecase.Deps{Orders: w.Orders(), Sagas: w.Sagas(), Keys: w.Keys(), Catalog: cat, Discounts: &fake.Discounts{Percent: map[string]int64{"s1/SALE10": 10}},
		Tx: w, Outbox: w, Clock: clock.NewFixed(time.Now()), IDs: idgen.UUID{}, FeeShip: 5000})
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer("order-service", metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	orderv1.RegisterOrderServiceServer(srv, transport.NewServer(uc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return grpcx.FromStatus(inv(ctx, m, req, reply, cc, o...))
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return orderv1.NewOrderServiceClient(conn), w
}

func as(id, typ string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), grpcx.MDAccountID, id, grpcx.MDAccountType, typ, grpcx.MDRoles, "x")
}

func shopOrders(code string) []*orderv1.ShopOrderInput {
	return []*orderv1.ShopOrderInput{{ShopId: "s1", DiscountCode: code, Items: []*orderv1.ItemInput{{ProductId: "p1", Quantity: 2}}}}
}

func TestOrdersOverGRPC(t *testing.T) {
	c, _ := start(t)
	alice := as("alice", "USER")

	rv, err := c.ReviewCheckout(alice, &orderv1.ReviewCheckoutRequest{ShopOrders: shopOrders("SALE10")})
	require.NoError(t, err)
	require.Len(t, rv.GetShops(), 1)
	assert.EqualValues(t, 200000, rv.GetShops()[0].GetSubtotal())
	assert.EqualValues(t, 20000, rv.GetShops()[0].GetDiscount())
	assert.Equal(t, "Shirt", rv.GetShops()[0].GetItems()[0].GetName())
	assert.EqualValues(t, 185000, rv.GetTotalCheckout(), "200000 - 20000 + 5000 shipping")

	req := &orderv1.PlaceOrderRequest{ShopOrders: shopOrders("SALE10"), Shipping: &orderv1.Shipping{Name: "An", Address: "1 Main St", City: "HCMC"},
		PaymentMethod: "card", PaymentToken: "tok_secret", IdempotencyKey: "key-1"}
	placed, err := c.PlaceOrder(alice, req)
	require.NoError(t, err)
	assert.Equal(t, "PENDING", placed.GetStatus())
	again, err := c.PlaceOrder(alice, req)
	require.NoError(t, err)
	assert.Equal(t, placed.GetOrderId(), again.GetOrderId())
	req.IdempotencyKey = ""
	_, err = c.PlaceOrder(alice, req)
	assert.Equal(t, domain.CodeIdempotencyKey, errs.CodeOf(err))

	got, err := c.GetOrder(alice, &orderv1.GetOrderRequest{OrderId: placed.GetOrderId()})
	require.NoError(t, err)
	assert.Equal(t, "alice", got.GetUserId())
	assert.Equal(t, "card", got.GetPaymentMethod())
	assert.Equal(t, "1 Main St", got.GetShipping().GetAddress())
	require.Len(t, got.GetItems(), 1)
	assert.EqualValues(t, 2, got.GetItems()[0].GetQuantity())
	assert.Equal(t, []string{"s1", "SALE10"}, []string{got.GetDiscounts()[0].GetShopId(), got.GetDiscounts()[0].GetCode()})
	assert.NotContains(t, got.String(), "tok_secret", "the payment token never leaves order-service")

	_, err = c.GetOrder(as("bob", "USER"), &orderv1.GetOrderRequest{OrderId: placed.GetOrderId()})
	assert.Equal(t, domain.CodeOrderNotFound, errs.CodeOf(err))

	list, err := c.ListOrders(alice, &orderv1.ListOrdersRequest{Page: 1, Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 1, list.GetTotal())
	require.Len(t, list.GetOrders(), 1)

	_, err = c.ReviewCheckout(as("shop1", "SHOP"), &orderv1.ReviewCheckoutRequest{ShopOrders: shopOrders("")})
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
}

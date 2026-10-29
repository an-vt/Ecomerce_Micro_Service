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

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/cart-service/internal/fake"
	transport "github.com/an-vt/ecom/cart-service/internal/transport/grpc"
	"github.com/an-vt/ecom/cart-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/metrics"
	cartv1 "github.com/an-vt/ecom/proto/gen/go/cart/v1"
)

func start(t *testing.T) cartv1.CartServiceClient {
	t.Helper()
	uc := usecase.NewSet(&usecase.Deps{Carts: fake.NewCarts(), Clock: clock.NewFixed(time.Now()), IDs: idgen.ULID{},
		Catalog: &fake.Catalog{Products_: map[string]domain.ProductInfo{"p1": {ID: "p1", ShopID: "s1", Name: "Shirt", Price: 1000, IsPublished: true}}},
		Stock:   &fake.Stock{Have: map[string]int64{"p1": 5}}})
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer("cart-service", metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	cartv1.RegisterCartServiceServer(srv, transport.NewServer(uc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return grpcx.FromStatus(inv(ctx, m, req, reply, cc, o...))
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return cartv1.NewCartServiceClient(conn)
}

func buyer(id string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), grpcx.MDAccountID, id, grpcx.MDAccountType, "USER", grpcx.MDRoles, "user")
}

func TestCartOverGRPC(t *testing.T) {
	c := start(t)
	empty, err := c.GetCart(buyer("u1"), &cartv1.GetCartRequest{})
	require.NoError(t, err)
	assert.Empty(t, empty.GetItems())
	assert.Equal(t, "u1", empty.GetUserId())

	got, err := c.AddToCart(buyer("u1"), &cartv1.AddToCartRequest{ProductId: "p1", Quantity: 2})
	require.NoError(t, err)
	require.Len(t, got.GetItems(), 1)
	assert.Equal(t, int64(2000), got.GetTotal())
	assert.Equal(t, int64(1), got.GetCountProduct())
	assert.NotEmpty(t, got.GetUpdatedAt())

	upd, err := c.UpdateCart(buyer("u1"), &cartv1.UpdateCartRequest{Lines: []*cartv1.UpdateLine{{ProductId: "p1", ShopId: "s1", Quantity: 4, OldQuantity: 2}}})
	require.NoError(t, err)
	assert.Equal(t, int64(4), upd.GetItems()[0].GetQuantity())
	_, err = c.UpdateCart(buyer("u1"), &cartv1.UpdateCartRequest{Lines: []*cartv1.UpdateLine{{ProductId: "p1", Quantity: 1, OldQuantity: 9}}})
	assert.Equal(t, domain.CodeCartChanged, errs.CodeOf(err))

	rm, err := c.RemoveItem(buyer("u1"), &cartv1.RemoveItemRequest{ProductId: "p1"})
	require.NoError(t, err)
	assert.Empty(t, rm.GetItems())

	_, err = c.AddToCart(buyer("u1"), &cartv1.AddToCartRequest{ProductId: "p1", Quantity: 99})
	assert.Equal(t, domain.CodeInsufficientStock, errs.CodeOf(err))
	_, err = c.AddToCart(context.Background(), &cartv1.AddToCartRequest{ProductId: "p1", Quantity: 1})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
}

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

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/inventory-service/internal/fake"
	transport "github.com/an-vt/ecom/inventory-service/internal/transport/grpc"
	"github.com/an-vt/ecom/inventory-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/metrics"
	inventoryv1 "github.com/an-vt/ecom/proto/gen/go/inventory/v1"
)

func start(t *testing.T) (inventoryv1.InventoryServiceClient, *usecase.Set) {
	t.Helper()
	w := fake.NewWorld()
	uc := usecase.NewSet(&usecase.Deps{Inventories: w.Inventories(), Reservations: w.Reservations(), Tx: w, Outbox: w,
		Clock: clock.NewFixed(time.Now()), IDs: idgen.ULID{}})
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer("inventory-service", metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	inventoryv1.RegisterInventoryServiceServer(srv, transport.NewServer(uc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return grpcx.FromStatus(inv(ctx, m, req, reply, cc, o...))
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return inventoryv1.NewInventoryServiceClient(conn), uc
}

func asShop(id string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), grpcx.MDAccountID, id, grpcx.MDAccountType, "SHOP", grpcx.MDRoles, "shop")
}

func TestStockOverGRPC(t *testing.T) {
	c, uc := start(t)
	require.NoError(t, uc.CreateInventory.Execute(context.Background(), usecase.ProductCreated{ProductID: "p1", ShopID: "shop1", Quantity: 10}))

	got, err := c.GetStock(context.Background(), &inventoryv1.GetStockRequest{ProductId: "p1"})
	require.NoError(t, err)
	assert.Equal(t, int64(10), got.GetStock())

	added, err := c.AddStock(asShop("shop1"), &inventoryv1.AddStockRequest{ProductId: "p1", Stock: 5, Location: "Hall 2"})
	require.NoError(t, err)
	assert.Equal(t, int64(15), added.GetStock())
	assert.Equal(t, "Hall 2", added.GetLocation())

	_, err = c.AddStock(asShop("shop2"), &inventoryv1.AddStockRequest{ProductId: "p1", Stock: 1})
	assert.Equal(t, domain.CodeNotOwner, errs.CodeOf(err))
	_, err = c.AddStock(context.Background(), &inventoryv1.AddStockRequest{ProductId: "p1", Stock: 1})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = c.GetStock(context.Background(), &inventoryv1.GetStockRequest{ProductId: "ghost"})
	assert.Equal(t, domain.CodeInventoryNotFound, errs.CodeOf(err))

	chk, err := c.CheckStock(context.Background(), &inventoryv1.CheckStockRequest{Lines: []*inventoryv1.StockLine{{ProductId: "p1", Quantity: 20}, {ProductId: "ghost", Quantity: 1}}})
	require.NoError(t, err)
	assert.False(t, chk.GetOk())
	require.Len(t, chk.GetShortages(), 2)
	assert.Equal(t, "ghost", chk.GetShortages()[0].GetProductId(), "shortages are sorted by product id")
	assert.Equal(t, int64(0), chk.GetShortages()[0].GetAvailable())
	assert.Equal(t, int64(15), chk.GetShortages()[1].GetAvailable())
	assert.Equal(t, int64(20), chk.GetShortages()[1].GetRequested())
	ok, err := c.CheckStock(context.Background(), &inventoryv1.CheckStockRequest{Lines: []*inventoryv1.StockLine{{ProductId: "p1", Quantity: 15}}})
	require.NoError(t, err)
	assert.True(t, ok.GetOk())
}

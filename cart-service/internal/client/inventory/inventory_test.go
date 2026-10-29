package inventoryclient_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/an-vt/ecom/cart-service/internal/client/clienttest"
	inventoryclient "github.com/an-vt/ecom/cart-service/internal/client/inventory"
	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	inventoryv1 "github.com/an-vt/ecom/proto/gen/go/inventory/v1"
)

type srv struct {
	inventoryv1.UnimplementedInventoryServiceServer
	ok   bool
	fail error
	got  []*inventoryv1.StockLine
}

func (s *srv) CheckStock(_ context.Context, in *inventoryv1.CheckStockRequest) (*inventoryv1.CheckStockResponse, error) {
	s.got = in.GetLines()
	if s.fail != nil {
		return nil, grpcx.ToStatus(s.fail)
	}
	return &inventoryv1.CheckStockResponse{Ok: s.ok}, nil
}

func client(t *testing.T, s *srv, reg *breaker.Registry) *inventoryclient.Client {
	return inventoryclient.New(inventoryv1.NewInventoryServiceClient(clienttest.Conn(t, func(g *grpc.Server) { inventoryv1.RegisterInventoryServiceServer(g, s) }, "inventory", reg)))
}

func TestLinesAndAnswerAreMapped(t *testing.T) {
	s := &srv{ok: true}
	c := client(t, s, nil)
	ok, err := c.Available(context.Background(), []domain.StockLine{{ProductID: "p1", Quantity: 3}, {ProductID: "p2", Quantity: 1}})
	require.NoError(t, err)
	assert.True(t, ok)
	require.Len(t, s.got, 2)
	assert.Equal(t, int64(3), s.got[0].GetQuantity())
	s.ok = false
	ok, err = c.Available(context.Background(), []domain.StockLine{{ProductID: "p1", Quantity: 99}})
	require.NoError(t, err)
	assert.False(t, ok)
}

// The adapter is only useful if an unhealthy dependency fails fast: the breaker opens and the cart answers 503
// instead of piling up slow calls.
func TestAnUnhealthyDependencyOpensTheCircuitAndFailsFast(t *testing.T) {
	reg := breaker.NewRegistry(grpcx.BreakerSettings(breaker.Settings{MinRequests: 3, ErrorRatio: 0.5, ResetTimeout: time.Minute}))
	c := client(t, &srv{fail: errs.Unavailable("X", "inventory down")}, reg)
	for i := 0; i < 6; i++ {
		_, err := c.Available(context.Background(), []domain.StockLine{{ProductID: "p", Quantity: 1}})
		require.Error(t, err)
	}
	assert.Equal(t, breaker.Open, reg.Get("inventory", "CheckStock").State())
	start := time.Now()
	_, err := c.Available(context.Background(), []domain.StockLine{{ProductID: "p", Quantity: 1}})
	assert.Less(t, time.Since(start), 50*time.Millisecond)
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err))
	_, isOpen := breaker.RetryAfter(err)
	assert.True(t, isOpen)
}

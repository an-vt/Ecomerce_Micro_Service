package catalogclient_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	catalogclient "github.com/an-vt/ecom/discount-service/internal/client/catalog"
	"github.com/an-vt/ecom/discount-service/internal/client/clienttest"
	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
)

type srv struct {
	catalogv1.UnimplementedCatalogServiceServer
	fail error
	got  []string
}

func (s *srv) GetProductsByIds(_ context.Context, in *catalogv1.GetProductsByIdsRequest) (*catalogv1.GetProductsByIdsResponse, error) {
	s.got = in.GetIds()
	if s.fail != nil {
		return nil, grpcx.ToStatus(s.fail)
	}
	return &catalogv1.GetProductsByIdsResponse{Products: []*catalogv1.Product{{Id: "p1", ShopId: "s1", Name: "Shirt", Price: 150000, Thumb: "t.jpg", IsPublished: true}}}, nil
}

func (s *srv) ListProductIdsByShop(_ context.Context, in *catalogv1.ListProductIdsByShopRequest) (*catalogv1.ListProductIdsByShopResponse, error) {
	if s.fail != nil {
		return nil, grpcx.ToStatus(s.fail)
	}
	return &catalogv1.ListProductIdsByShopResponse{Ids: []string{in.GetShopId() + "-1", in.GetShopId() + "-2"}}, nil
}

func client(t *testing.T, s *srv, reg *breaker.Registry) *catalogclient.Client {
	return catalogclient.New(catalogv1.NewCatalogServiceClient(clienttest.Conn(t, func(g *grpc.Server) { catalogv1.RegisterCatalogServiceServer(g, s) }, "catalog", reg)))
}

func TestProductsAreMapped(t *testing.T) {
	s := &srv{}
	got, err := client(t, s, nil).Products(context.Background(), []string{"p1", "ghost"})
	require.NoError(t, err)
	assert.Equal(t, []string{"p1", "ghost"}, s.got)
	assert.Equal(t, domain.ProductSummary{ID: "p1", ShopID: "s1", Name: "Shirt", Price: 150000, Thumb: "t.jpg", IsPublished: true}, got["p1"])
	assert.NotContains(t, got, "ghost")
}

func TestProductIDsByShop(t *testing.T) {
	ids, err := client(t, &srv{}, nil).ProductIDsByShop(context.Background(), "shopx")
	require.NoError(t, err)
	assert.Equal(t, []string{"shopx-1", "shopx-2"}, ids)
}

func TestErrorsKeepTheirKindAndCode(t *testing.T) {
	c := client(t, &srv{fail: errs.InvalidArgument("TOO_MANY", "at most 200 ids")}, nil)
	_, err := c.Products(context.Background(), []string{"p"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	assert.Equal(t, "TOO_MANY", errs.CodeOf(err))
	_, err = c.ProductIDsByShop(context.Background(), "s")
	assert.Equal(t, "TOO_MANY", errs.CodeOf(err))
}

func TestAnUnhealthyCatalogOpensTheCircuit(t *testing.T) {
	reg := breaker.NewRegistry(grpcx.BreakerSettings(breaker.Settings{MinRequests: 3, ErrorRatio: 0.5, ResetTimeout: time.Minute}))
	c := client(t, &srv{fail: errs.Unavailable("X", "catalog down")}, reg)
	for i := 0; i < 6; i++ {
		_, err := c.ProductIDsByShop(context.Background(), "s")
		require.Error(t, err)
	}
	start := time.Now()
	_, err := c.ProductIDsByShop(context.Background(), "s")
	assert.Less(t, time.Since(start), 50*time.Millisecond, "fails fast once open")
	_, isOpen := breaker.RetryAfter(err)
	assert.True(t, isOpen)
}

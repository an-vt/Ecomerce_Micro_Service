package catalogclient_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	catalogclient "github.com/an-vt/ecom/cart-service/internal/client/catalog"
	"github.com/an-vt/ecom/cart-service/internal/client/clienttest"
	"github.com/an-vt/ecom/cart-service/internal/domain"
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
	return &catalogv1.GetProductsByIdsResponse{Products: []*catalogv1.Product{{Id: "p1", ShopId: "s1", Name: "Shirt", Price: 150000, IsPublished: true}}}, nil
}

func client(t *testing.T, s *srv) *catalogclient.Client {
	return catalogclient.New(catalogv1.NewCatalogServiceClient(clienttest.Conn(t, func(g *grpc.Server) { catalogv1.RegisterCatalogServiceServer(g, s) }, "catalog", nil)))
}

func TestProductsAreMapped(t *testing.T) {
	s := &srv{}
	got, err := client(t, s).Products(context.Background(), []string{"p1", "ghost"})
	require.NoError(t, err)
	assert.Equal(t, []string{"p1", "ghost"}, s.got)
	assert.Equal(t, domain.ProductInfo{ID: "p1", ShopID: "s1", Name: "Shirt", Price: 150000, IsPublished: true}, got["p1"])
	assert.NotContains(t, got, "ghost")
}

func TestBusinessErrorsKeepTheirKindAndCode(t *testing.T) {
	_, err := client(t, &srv{fail: errs.InvalidArgument("TOO_MANY", "at most 200 ids")}).Products(context.Background(), []string{"p"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	assert.Equal(t, "TOO_MANY", errs.CodeOf(err))
}

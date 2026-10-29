package discountclient_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/an-vt/ecom/order-service/internal/client/clienttest"
	discountclient "github.com/an-vt/ecom/order-service/internal/client/discount"
	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/principal"
	discountv1 "github.com/an-vt/ecom/proto/gen/go/discount/v1"
)

type srv struct {
	discountv1.UnimplementedDiscountServiceServer
	fail error
	got  *discountv1.CalculateDiscountRequest
	md   metadata.MD
}

func (s *srv) CalculateDiscount(ctx context.Context, in *discountv1.CalculateDiscountRequest) (*discountv1.CalculateDiscountResponse, error) {
	s.got = in
	s.md, _ = metadata.FromIncomingContext(ctx)
	if s.fail != nil {
		return nil, grpcx.ToStatus(s.fail)
	}
	return &discountv1.CalculateDiscountResponse{Discount: 20000, Subtotal: 200000, Total: 180000}, nil
}

func client(t *testing.T, s *srv) *discountclient.Client {
	return discountclient.New(discountv1.NewDiscountServiceClient(clienttest.Conn(t, func(g *grpc.Server) { discountv1.RegisterDiscountServiceServer(g, s) }, "discount", nil)))
}

func TestPreviewMapsLinesAndForwardsTheBuyer(t *testing.T) {
	s := &srv{}
	ctx := principal.With(context.Background(), principal.Principal{ID: "alice", Type: principal.TypeUser})
	got, err := client(t, s).Preview(ctx, "s1", "SALE10", []domain.PreviewLine{{ProductID: "p1", Price: 100000, Quantity: 2}})
	require.NoError(t, err)
	assert.Equal(t, domain.DiscountPreview{Discount: 20000, Subtotal: 200000, Total: 180000}, got)
	assert.Equal(t, "SALE10", s.got.GetCode())
	assert.Equal(t, "s1", s.got.GetShopId())
	require.Len(t, s.got.GetLines(), 1)
	assert.EqualValues(t, 100000, s.got.GetLines()[0].GetPrice())
	assert.Equal(t, "alice", s.md.Get(grpcx.MDAccountID)[0], "the buyer's own earlier uses are counted by discount-service")
}

func TestBusinessErrorsKeepTheirCode(t *testing.T) {
	_, err := client(t, &srv{fail: errs.FailedPrecondition("DISCOUNT_EXPIRED", "expired")}).Preview(context.Background(), "s", "C", nil)
	assert.Equal(t, errs.KindFailedPrecondition, errs.KindOf(err))
	assert.Equal(t, "DISCOUNT_EXPIRED", errs.CodeOf(err))
}

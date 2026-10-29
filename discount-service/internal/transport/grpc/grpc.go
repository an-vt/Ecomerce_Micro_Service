// Package grpc exposes the discount use cases as the DiscountService gRPC service (proto/discount/v1).
package grpc

import (
	"context"
	"time"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/discount-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
	discountv1 "github.com/an-vt/ecom/proto/gen/go/discount/v1"
)

// Server implements discountv1.DiscountServiceServer.
type Server struct {
	discountv1.UnimplementedDiscountServiceServer
	uc *usecase.Set
}

// NewServer creates the handler.
func NewServer(uc *usecase.Set) *Server { return &Server{uc: uc} }

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func toDiscount(v usecase.DiscountView) *discountv1.Discount {
	return &discountv1.Discount{Id: v.ID, ShopId: v.ShopID, Code: v.Code, Name: v.Name, Description: v.Description, Type: string(v.Type),
		Value: v.Value, StartDate: rfc(v.StartDate), EndDate: rfc(v.EndDate), MaxUses: v.MaxUses, UsedCount: v.UsedCount,
		MaxUsesPerUser: v.MaxUsesPerUser, MinOrderValue: v.MinOrderValue, IsActive: v.IsActive, AppliesTo: string(v.AppliesTo), ProductIds: v.ProductIDs}
}

func parseTime(field, s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errs.Validation(field + " must be an RFC 3339 timestamp")
	}
	return t, nil
}

func (s *Server) CreateDiscount(ctx context.Context, in *discountv1.CreateDiscountRequest) (*discountv1.Discount, error) {
	start, err := parseTime("start_date", in.GetStartDate())
	if err != nil {
		return nil, err
	}
	end, err := parseTime("end_date", in.GetEndDate())
	if err != nil {
		return nil, err
	}
	v, err := s.uc.CreateDiscount.Execute(ctx, usecase.CreateInput{Code: in.GetCode(), Name: in.GetName(), Description: in.GetDescription(),
		Type: in.GetType(), Value: in.GetValue(), StartDate: start, EndDate: end, MaxUses: in.GetMaxUses(), MaxUsesPerUser: in.GetMaxUsesPerUser(),
		MinOrderValue: in.GetMinOrderValue(), AppliesTo: in.GetAppliesTo(), ProductIDs: in.GetProductIds()})
	if err != nil {
		return nil, err
	}
	return toDiscount(v), nil
}

func (s *Server) UpdateDiscount(ctx context.Context, in *discountv1.UpdateDiscountRequest) (*discountv1.Discount, error) {
	u := usecase.UpdateInput{DiscountID: in.GetDiscountId(), Name: in.Name, Description: in.Description, IsActive: in.IsActive,
		MaxUses: in.MaxUses, MaxUsesPerUser: in.MaxUsesPerUser, MinOrderValue: in.MinOrderValue, AppliesTo: in.AppliesTo,
		ProductIDs: in.GetProductIds(), ReplaceProductIDs: in.GetReplaceProductIds()}
	if in.EndDate != nil {
		end, err := parseTime("end_date", in.GetEndDate())
		if err != nil {
			return nil, err
		}
		u.EndDate = &end
	}
	v, err := s.uc.UpdateDiscount.Execute(ctx, u)
	if err != nil {
		return nil, err
	}
	return toDiscount(v), nil
}

func (s *Server) ListShopDiscounts(ctx context.Context, in *discountv1.ListShopDiscountsRequest) (*discountv1.ListShopDiscountsResponse, error) {
	res, err := s.uc.ListShopDiscounts.Execute(ctx, int(in.GetPage()), int(in.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := &discountv1.ListShopDiscountsResponse{Total: res.Total}
	for _, d := range res.Items {
		out.Discounts = append(out.Discounts, toDiscount(d))
	}
	return out, nil
}

func (s *Server) ListProductsForCode(ctx context.Context, in *discountv1.ListProductsForCodeRequest) (*discountv1.ListProductsForCodeResponse, error) {
	res, err := s.uc.ListProductsForCode.Execute(ctx, in.GetShopId(), in.GetCode(), int(in.GetPage()), int(in.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := &discountv1.ListProductsForCodeResponse{Total: res.Total}
	for _, p := range res.Items {
		out.Products = append(out.Products, &discountv1.ProductSummary{Id: p.ID, Name: p.Name, Price: int64(p.Price), Thumb: p.Thumb})
	}
	return out, nil
}

func (s *Server) CalculateDiscount(ctx context.Context, in *discountv1.CalculateDiscountRequest) (*discountv1.CalculateDiscountResponse, error) {
	lines := make([]domain.Line, len(in.GetLines()))
	for i, l := range in.GetLines() {
		lines[i] = domain.Line{ProductID: l.GetProductId(), Price: money.Amount(l.GetPrice()), Quantity: l.GetQuantity()}
	}
	p, err := s.uc.CalculateDiscount.Execute(ctx, in.GetShopId(), in.GetCode(), lines)
	if err != nil {
		return nil, err
	}
	return &discountv1.CalculateDiscountResponse{Discount: p.Discount, Subtotal: p.Subtotal, Total: p.Total}, nil
}

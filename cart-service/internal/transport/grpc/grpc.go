// Package grpc exposes the cart use cases as the CartService gRPC service (proto/cart/v1).
package grpc

import (
	"context"
	"time"

	"github.com/an-vt/ecom/cart-service/internal/usecase"
	cartv1 "github.com/an-vt/ecom/proto/gen/go/cart/v1"
)

// Server implements cartv1.CartServiceServer.
type Server struct {
	cartv1.UnimplementedCartServiceServer
	uc *usecase.Set
}

// NewServer creates the handler.
func NewServer(uc *usecase.Set) *Server { return &Server{uc: uc} }

func toCart(v usecase.CartView) *cartv1.Cart {
	out := &cartv1.Cart{Id: v.ID, UserId: v.UserID, State: v.State, CountProduct: v.Lines, Total: v.Total}
	if !v.UpdatedAt.IsZero() {
		out.UpdatedAt = v.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	for _, it := range v.Items {
		out.Items = append(out.Items, &cartv1.CartItem{ProductId: it.ProductID, ShopId: it.ShopID, Name: it.Name, Price: it.Price, Quantity: it.Quantity})
	}
	return out
}

func (s *Server) AddToCart(ctx context.Context, in *cartv1.AddToCartRequest) (*cartv1.Cart, error) {
	v, err := s.uc.AddToCart.Execute(ctx, in.GetProductId(), in.GetQuantity())
	if err != nil {
		return nil, err
	}
	return toCart(v), nil
}

func (s *Server) UpdateCart(ctx context.Context, in *cartv1.UpdateCartRequest) (*cartv1.Cart, error) {
	lines := make([]usecase.UpdateLine, len(in.GetLines()))
	for i, l := range in.GetLines() {
		lines[i] = usecase.UpdateLine{ProductID: l.GetProductId(), ShopID: l.GetShopId(), Quantity: l.GetQuantity(), OldQuantity: l.GetOldQuantity()}
	}
	v, err := s.uc.UpdateCart.Execute(ctx, lines)
	if err != nil {
		return nil, err
	}
	return toCart(v), nil
}

func (s *Server) RemoveItem(ctx context.Context, in *cartv1.RemoveItemRequest) (*cartv1.Cart, error) {
	v, err := s.uc.RemoveItem.Execute(ctx, in.GetProductId())
	if err != nil {
		return nil, err
	}
	return toCart(v), nil
}

func (s *Server) GetCart(ctx context.Context, _ *cartv1.GetCartRequest) (*cartv1.Cart, error) {
	v, err := s.uc.GetCart.Execute(ctx)
	if err != nil {
		return nil, err
	}
	return toCart(v), nil
}

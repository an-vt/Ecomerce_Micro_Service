// Package grpc exposes the order use cases as the OrderService gRPC service (proto/order/v1).
package grpc

import (
	"context"
	"time"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/order-service/internal/usecase"
	orderv1 "github.com/an-vt/ecom/proto/gen/go/order/v1"
)

// Server implements orderv1.OrderServiceServer.
type Server struct {
	orderv1.UnimplementedOrderServiceServer
	uc *usecase.Set
}

// NewServer creates the handler.
func NewServer(uc *usecase.Set) *Server { return &Server{uc: uc} }

func shopOrders(in []*orderv1.ShopOrderInput) []usecase.ShopOrder {
	out := make([]usecase.ShopOrder, len(in))
	for i, so := range in {
		items := make([]usecase.ItemInput, len(so.GetItems()))
		for j, it := range so.GetItems() {
			items[j] = usecase.ItemInput{ProductID: it.GetProductId(), Quantity: it.GetQuantity()}
		}
		out[i] = usecase.ShopOrder{ShopID: so.GetShopId(), DiscountCode: so.GetDiscountCode(), Items: items}
	}
	return out
}

func (s *Server) ReviewCheckout(ctx context.Context, in *orderv1.ReviewCheckoutRequest) (*orderv1.ReviewCheckoutResponse, error) {
	rv, err := s.uc.ReviewCheckout.Execute(ctx, shopOrders(in.GetShopOrders()))
	if err != nil {
		return nil, err
	}
	out := &orderv1.ReviewCheckoutResponse{TotalPrice: rv.TotalPrice, TotalDiscount: rv.TotalDiscount, FeeShip: rv.FeeShip, TotalCheckout: rv.TotalCheckout}
	for _, sh := range rv.Shops {
		rs := &orderv1.ReviewShop{ShopId: sh.ShopID, Subtotal: sh.Subtotal, DiscountCode: sh.DiscountCode, Discount: sh.Discount, Total: sh.Total}
		for _, it := range sh.Items {
			rs.Items = append(rs.Items, &orderv1.ReviewItem{ProductId: it.ProductID, Name: it.Name, Price: it.Price, Quantity: it.Quantity})
		}
		out.Shops = append(out.Shops, rs)
	}
	return out, nil
}

func (s *Server) PlaceOrder(ctx context.Context, in *orderv1.PlaceOrderRequest) (*orderv1.PlaceOrderResponse, error) {
	sh := in.GetShipping()
	p, err := s.uc.PlaceOrder.Execute(ctx, usecase.PlaceInput{ShopOrders: shopOrders(in.GetShopOrders()),
		Shipping:      domain.Shipping{Name: sh.GetName(), Phone: sh.GetPhone(), Address: sh.GetAddress(), City: sh.GetCity()},
		PaymentMethod: in.GetPaymentMethod(), PaymentToken: in.GetPaymentToken(), IdempotencyKey: in.GetIdempotencyKey()})
	if err != nil {
		return nil, err
	}
	return &orderv1.PlaceOrderResponse{OrderId: p.OrderID, Status: string(p.Status)}, nil
}

func (s *Server) GetOrder(ctx context.Context, in *orderv1.GetOrderRequest) (*orderv1.Order, error) {
	v, err := s.uc.GetOrder.Execute(ctx, in.GetOrderId())
	if err != nil {
		return nil, err
	}
	return toOrder(v), nil
}

func (s *Server) ListOrders(ctx context.Context, in *orderv1.ListOrdersRequest) (*orderv1.ListOrdersResponse, error) {
	res, err := s.uc.ListOrders.Execute(ctx, int(in.GetPage()), int(in.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := &orderv1.ListOrdersResponse{Total: res.Total}
	for _, v := range res.Items {
		out.Orders = append(out.Orders, toOrder(v))
	}
	return out, nil
}

// toOrder maps a view to the wire. The payment token is not part of the view, so it cannot leak from here.
func toOrder(v usecase.OrderView) *orderv1.Order {
	o := &orderv1.Order{Id: v.ID, UserId: v.UserID, Status: string(v.Status), TotalPrice: v.TotalPrice, TotalDiscount: v.TotalDiscount, FeeShip: v.FeeShip,
		TotalCheckout: v.TotalCheckout, PaymentMethod: v.PaymentMethod, CancelReason: v.CancelReason, TrackingNumber: v.TrackingNumber,
		CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: v.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Shipping: &orderv1.Shipping{Name: v.Shipping.Name, Phone: v.Shipping.Phone, Address: v.Shipping.Address, City: v.Shipping.City}}
	for _, it := range v.Items {
		o.Items = append(o.Items, &orderv1.OrderItem{ProductId: it.ProductID, ShopId: it.ShopID, Name: it.Name, Price: it.Price, Quantity: it.Quantity})
	}
	for _, d := range v.Discounts {
		o.Discounts = append(o.Discounts, &orderv1.DiscountRef{ShopId: d.ShopID, Code: d.Code})
	}
	return o
}

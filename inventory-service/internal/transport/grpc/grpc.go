// Package grpc exposes the stock use cases as the InventoryService gRPC service (proto/inventory/v1).
package grpc

import (
	"context"

	"github.com/an-vt/ecom/inventory-service/internal/usecase"
	inventoryv1 "github.com/an-vt/ecom/proto/gen/go/inventory/v1"
)

// Server implements inventoryv1.InventoryServiceServer.
type Server struct {
	inventoryv1.UnimplementedInventoryServiceServer
	uc *usecase.Set
}

// NewServer creates the handler.
func NewServer(uc *usecase.Set) *Server { return &Server{uc: uc} }

func toStock(s usecase.Stock) *inventoryv1.Stock {
	return &inventoryv1.Stock{ProductId: s.ProductID, ShopId: s.ShopID, Location: s.Location, Stock: s.Stock, Reserved: s.Reserved}
}

func (s *Server) AddStock(ctx context.Context, in *inventoryv1.AddStockRequest) (*inventoryv1.Stock, error) {
	st, err := s.uc.AddStock.Execute(ctx, usecase.AddStockInput{ProductID: in.GetProductId(), Amount: in.GetStock(), Location: in.GetLocation()})
	if err != nil {
		return nil, err
	}
	return toStock(st), nil
}

func (s *Server) GetStock(ctx context.Context, in *inventoryv1.GetStockRequest) (*inventoryv1.Stock, error) {
	st, err := s.uc.GetStock.Execute(ctx, in.GetProductId())
	if err != nil {
		return nil, err
	}
	return toStock(st), nil
}

func (s *Server) CheckStock(ctx context.Context, in *inventoryv1.CheckStockRequest) (*inventoryv1.CheckStockResponse, error) {
	lines := make([]usecase.Line, len(in.GetLines()))
	for i, l := range in.GetLines() {
		lines[i] = usecase.Line{ProductID: l.GetProductId(), Quantity: l.GetQuantity()}
	}
	ok, short, err := s.uc.CheckStock.Execute(ctx, lines)
	if err != nil {
		return nil, err
	}
	out := &inventoryv1.CheckStockResponse{Ok: ok}
	for _, sh := range short {
		out.Shortages = append(out.Shortages, &inventoryv1.Shortage{ProductId: sh.ProductID, Requested: sh.Requested, Available: sh.Available})
	}
	return out, nil
}

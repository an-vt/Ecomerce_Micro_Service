// Package grpc exposes the use cases as the CatalogService gRPC service (proto/catalog/v1). Handlers translate
// between protobuf messages and use-case inputs and nothing else.
package grpc

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/catalog-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/errs"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
)

// Server implements catalogv1.CatalogServiceServer.
type Server struct {
	catalogv1.UnimplementedCatalogServiceServer
	uc *usecase.Set
}

// NewServer creates the handler.
func NewServer(uc *usecase.Set) *Server { return &Server{uc: uc} }

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// attrsFromJSON parses the free-form attributes; empty means none.
func attrsFromJSON(s string) (map[string]any, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, errs.InvalidArgument(domain.CodeInvalidAttribute, "attributes_json is not a valid JSON object")
	}
	return m, nil
}

func attrsToJSON(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func toProduct(v usecase.ProductView) *catalogv1.Product {
	return &catalogv1.Product{Id: v.ID, ShopId: v.ShopID, ShopName: v.ShopName, Name: v.Name, Description: v.Description, Slug: v.Slug,
		Price: v.Price, Quantity: v.Quantity, Type: v.Type, Thumb: v.Thumb, AttributesJson: attrsToJSON(v.Attributes),
		RatingsAverage: v.RatingsAverage, IsDraft: v.IsDraft, IsPublished: v.IsPublished, CreatedAt: ts(v.CreatedAt), UpdatedAt: ts(v.UpdatedAt)}
}

func toList(p usecase.Page[usecase.ProductView]) *catalogv1.ListProductsResponse {
	out := &catalogv1.ListProductsResponse{Total: p.Total, Page: int32(p.Page), Limit: int32(p.Limit)}
	for _, v := range p.Items {
		out.Products = append(out.Products, toProduct(v))
	}
	return out
}

func (s *Server) CreateProduct(ctx context.Context, in *catalogv1.CreateProductRequest) (*catalogv1.Product, error) {
	attrs, err := attrsFromJSON(in.GetAttributesJson())
	if err != nil {
		return nil, err
	}
	v, err := s.uc.CreateProduct.Execute(ctx, usecase.CreateProductInput{Type: in.GetType(), Name: in.GetName(), Description: in.GetDescription(),
		Price: in.GetPrice(), Quantity: in.GetQuantity(), Thumb: in.GetThumb(), Attributes: attrs})
	if err != nil {
		return nil, err
	}
	return toProduct(v), nil
}

func (s *Server) UpdateProduct(ctx context.Context, in *catalogv1.UpdateProductRequest) (*catalogv1.Product, error) {
	var attrs map[string]any
	if in.AttributesJson != nil {
		var err error
		if attrs, err = attrsFromJSON(in.GetAttributesJson()); err != nil {
			return nil, err
		}
	}
	v, err := s.uc.UpdateProduct.Execute(ctx, usecase.UpdateProductInput{ProductID: in.GetProductId(), Name: in.Name, Description: in.Description,
		Thumb: in.Thumb, Price: in.Price, Quantity: in.Quantity, Attributes: attrs})
	if err != nil {
		return nil, err
	}
	return toProduct(v), nil
}

func (s *Server) PublishProduct(ctx context.Context, in *catalogv1.ProductIdRequest) (*catalogv1.Product, error) {
	v, err := s.uc.SetPublished.Execute(ctx, in.GetProductId(), true)
	if err != nil {
		return nil, err
	}
	return toProduct(v), nil
}

func (s *Server) UnpublishProduct(ctx context.Context, in *catalogv1.ProductIdRequest) (*catalogv1.Product, error) {
	v, err := s.uc.SetPublished.Execute(ctx, in.GetProductId(), false)
	if err != nil {
		return nil, err
	}
	return toProduct(v), nil
}

func (s *Server) GetProduct(ctx context.Context, in *catalogv1.ProductIdRequest) (*catalogv1.Product, error) {
	v, err := s.uc.GetProduct.Execute(ctx, in.GetProductId())
	if err != nil {
		return nil, err
	}
	return toProduct(v), nil
}

func (s *Server) ListProducts(ctx context.Context, in *catalogv1.ListProductsRequest) (*catalogv1.ListProductsResponse, error) {
	p, err := s.uc.ListProducts.Execute(ctx, int(in.GetPage()), int(in.GetLimit()), in.GetSort())
	if err != nil {
		return nil, err
	}
	return toList(p), nil
}

func (s *Server) SearchProducts(ctx context.Context, in *catalogv1.SearchProductsRequest) (*catalogv1.ListProductsResponse, error) {
	p, err := s.uc.SearchProducts.Execute(ctx, in.GetKeyword(), int(in.GetPage()), int(in.GetLimit()))
	if err != nil {
		return nil, err
	}
	return toList(p), nil
}

func (s *Server) ListShopProducts(ctx context.Context, in *catalogv1.ListShopProductsRequest) (*catalogv1.ListProductsResponse, error) {
	p, err := s.uc.ListShopProducts.Execute(ctx, in.GetPublished(), int(in.GetPage()), int(in.GetLimit()))
	if err != nil {
		return nil, err
	}
	return toList(p), nil
}

func (s *Server) GetProductsByIds(ctx context.Context, in *catalogv1.GetProductsByIdsRequest) (*catalogv1.GetProductsByIdsResponse, error) {
	vs, err := s.uc.GetProductsByIds.Execute(ctx, in.GetIds())
	if err != nil {
		return nil, err
	}
	out := &catalogv1.GetProductsByIdsResponse{}
	for _, v := range vs {
		out.Products = append(out.Products, toProduct(v))
	}
	return out, nil
}

func (s *Server) ListProductIdsByShop(ctx context.Context, in *catalogv1.ListProductIdsByShopRequest) (*catalogv1.ListProductIdsByShopResponse, error) {
	ids, err := s.uc.ListProductIdsByShop.Execute(ctx, in.GetShopId())
	if err != nil {
		return nil, err
	}
	return &catalogv1.ListProductIdsByShopResponse{Ids: ids}, nil
}

// ---- SPU / SKU -----------------------------------------------------------------------------------------------

func toSpuResponse(spu *domain.Spu, skus []*domain.Sku) *catalogv1.SpuResponse {
	out := &catalogv1.SpuResponse{Spu: &catalogv1.Spu{Id: spu.ID, ShopId: spu.ShopID, Name: spu.Name, Thumb: spu.Thumb, Description: spu.Description,
		Slug: spu.Slug, Price: int64(spu.Price), Categories: spu.Categories, Quantity: spu.Quantity, AttributesJson: attrsToJSON(spu.Attributes),
		RatingsAverage: spu.RatingsAverage, IsDraft: spu.IsDraft, IsPublished: spu.IsPublished}}
	for _, v := range spu.Variations {
		out.Spu.Variations = append(out.Spu.Variations, &catalogv1.Variation{Name: v.Name, Options: v.Options, Images: v.Images})
	}
	for _, s := range skus {
		out.Skus = append(out.Skus, toSku(s))
	}
	return out
}

func toSku(s *domain.Sku) *catalogv1.Sku {
	tiers := make([]int32, len(s.TierIdx))
	for i, t := range s.TierIdx {
		tiers[i] = int32(t)
	}
	return &catalogv1.Sku{Id: s.ID, SpuId: s.SpuID, TierIdx: tiers, IsDefault: s.IsDefault, Slug: s.Slug, Sort: int32(s.Sort), Price: int64(s.Price), Stock: s.Stock}
}

func (s *Server) CreateSpu(ctx context.Context, in *catalogv1.CreateSpuRequest) (*catalogv1.SpuResponse, error) {
	attrs, err := attrsFromJSON(in.GetAttributesJson())
	if err != nil {
		return nil, err
	}
	input := domain.SpuInput{Name: in.GetName(), Thumb: in.GetThumb(), Description: in.GetDescription(), Price: in.GetPrice(),
		Categories: in.GetCategories(), Quantity: in.GetQuantity(), Attributes: attrs}
	for _, v := range in.GetVariations() {
		input.Variations = append(input.Variations, domain.Variation{Name: v.GetName(), Options: v.GetOptions(), Images: v.GetImages()})
	}
	for _, k := range in.GetSkus() {
		tiers := make([]int, len(k.GetTierIdx()))
		for i, t := range k.GetTierIdx() {
			tiers[i] = int(t)
		}
		input.Skus = append(input.Skus, domain.SkuInput{TierIdx: tiers, IsDefault: k.GetIsDefault(), Sort: int(k.GetSort()), Price: k.GetPrice(), Stock: k.GetStock()})
	}
	spu, skus, err := s.uc.CreateSpu.Execute(ctx, input)
	if err != nil {
		return nil, err
	}
	return toSpuResponse(spu, skus), nil
}

func (s *Server) GetSpu(ctx context.Context, in *catalogv1.GetSpuRequest) (*catalogv1.SpuResponse, error) {
	spu, skus, err := s.uc.GetSpu.Execute(ctx, in.GetSpuId())
	if err != nil {
		return nil, err
	}
	return toSpuResponse(spu, skus), nil
}

func (s *Server) GetSku(ctx context.Context, in *catalogv1.GetSkuRequest) (*catalogv1.Sku, error) {
	sku, err := s.uc.GetSku.Execute(ctx, in.GetSkuId(), in.GetSpuId())
	if err != nil {
		return nil, err
	}
	return toSku(sku), nil
}

// ---- comments ------------------------------------------------------------------------------------------------

func toComment(c *domain.Comment) *catalogv1.Comment {
	return &catalogv1.Comment{Id: c.ID, ProductId: c.ProductID, UserId: c.UserID, Content: c.Content, Left: c.Left, Right: c.Right,
		ParentId: c.ParentID, CreatedAt: ts(c.CreatedAt)}
}

func (s *Server) CreateComment(ctx context.Context, in *catalogv1.CreateCommentRequest) (*catalogv1.Comment, error) {
	c, err := s.uc.CreateComment.Execute(ctx, usecase.CreateCommentInput{ProductID: in.GetProductId(), Content: in.GetContent(), ParentCommentID: in.GetParentCommentId()})
	if err != nil {
		return nil, err
	}
	return toComment(c), nil
}

func (s *Server) DeleteComment(ctx context.Context, in *catalogv1.DeleteCommentRequest) (*emptypb.Empty, error) {
	if err := s.uc.DeleteComment.Execute(ctx, in.GetCommentId(), in.GetProductId()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) ListComments(ctx context.Context, in *catalogv1.ListCommentsRequest) (*catalogv1.ListCommentsResponse, error) {
	cs, err := s.uc.ListComments.Execute(ctx, in.GetProductId(), in.GetParentId(), int(in.GetLimit()), int(in.GetSkip()))
	if err != nil {
		return nil, err
	}
	out := &catalogv1.ListCommentsResponse{}
	for _, c := range cs {
		out.Comments = append(out.Comments, toComment(c))
	}
	return out, nil
}

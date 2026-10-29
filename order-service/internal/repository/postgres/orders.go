package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type shippingJSON struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	Address string `json:"address"`
	City    string `json:"city"`
}

type discountJSON struct {
	ShopID string `json:"shopId"`
	Code   string `json:"code"`
}

type orderRow struct {
	bun.BaseModel `bun:"table:orders"`

	ID             string         `bun:"id,pk"`
	UserID         string         `bun:"user_id"`
	Status         string         `bun:"status"`
	TotalPrice     int64          `bun:"total_price"`
	TotalDiscount  int64          `bun:"total_discount"`
	FeeShip        int64          `bun:"fee_ship"`
	TotalCheckout  int64          `bun:"total_checkout"`
	Shipping       shippingJSON   `bun:"shipping,type:jsonb"`
	PaymentMethod  string         `bun:"payment_method"`
	PaymentToken   string         `bun:"payment_token"`
	Discounts      []discountJSON `bun:"discounts,type:jsonb"`
	TrackingNumber *string        `bun:"tracking_number"`
	CancelReason   *string        `bun:"cancel_reason"`
	Version        int64          `bun:"version"`
	CreatedAt      time.Time      `bun:"created_at"`
	UpdatedAt      time.Time      `bun:"updated_at"`
}

type itemRow struct {
	bun.BaseModel `bun:"table:order_items"`

	ID        string `bun:"id,pk"`
	OrderID   string `bun:"order_id"`
	Position  int    `bun:"position"`
	ProductID string `bun:"product_id"`
	ShopID    string `bun:"shop_id"`
	Name      string `bun:"name"`
	Price     int64  `bun:"price"`
	Quantity  int64  `bun:"quantity"`
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (r *orderRow) toDomain(items []itemRow) *domain.Order {
	s := domain.OrderSnapshot{ID: r.ID, UserID: r.UserID, Status: domain.Status(r.Status), TotalPrice: r.TotalPrice, TotalDiscount: r.TotalDiscount,
		FeeShip: r.FeeShip, TotalCheckout: r.TotalCheckout, Shipping: domain.Shipping(r.Shipping), PaymentMethod: r.PaymentMethod,
		PaymentToken: r.PaymentToken, TrackingNumber: str(r.TrackingNumber), CancelReason: str(r.CancelReason), Version: r.Version,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
	for _, d := range r.Discounts {
		s.Discounts = append(s.Discounts, domain.DiscountRef(d))
	}
	for _, it := range items {
		s.Items = append(s.Items, domain.Item{ProductID: it.ProductID, ShopID: it.ShopID, Name: it.Name, Price: it.Price, Quantity: it.Quantity})
	}
	return domain.RestoreOrder(s)
}

type orderRepo struct{ s *pgdb.Store }

func (r *orderRepo) Insert(ctx context.Context, o *domain.Order) error {
	s := o.Snapshot()
	discounts := make([]discountJSON, len(s.Discounts))
	for i, d := range s.Discounts {
		discounts[i] = discountJSON(d)
	}
	row := &orderRow{ID: s.ID, UserID: s.UserID, Status: string(s.Status), TotalPrice: s.TotalPrice, TotalDiscount: s.TotalDiscount, FeeShip: s.FeeShip,
		TotalCheckout: s.TotalCheckout, Shipping: shippingJSON(s.Shipping), PaymentMethod: s.PaymentMethod, PaymentToken: s.PaymentToken,
		Discounts: discounts, TrackingNumber: nullable(s.TrackingNumber), CancelReason: nullable(s.CancelReason), Version: s.Version,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	db := r.s.DB(ctx)
	if _, err := db.NewInsert().Model(row).Exec(ctx); err != nil {
		return err
	}
	items := make([]itemRow, len(s.Items))
	for i, it := range s.Items {
		items[i] = itemRow{ID: idgen.UUID{}.New(), OrderID: s.ID, Position: i, ProductID: it.ProductID, ShopID: it.ShopID, Name: it.Name, Price: it.Price, Quantity: it.Quantity}
	}
	_, err := db.NewInsert().Model(&items).Exec(ctx)
	return err
}

func (r *orderRepo) load(ctx context.Context, id string, lock bool) (*domain.Order, error) {
	row := new(orderRow)
	q := r.s.DB(ctx).NewSelect().Model(row).Where("id = ?", id).Limit(1)
	if lock {
		q = q.For("UPDATE")
	}
	if err := q.Scan(ctx); errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound()
	} else if err != nil {
		return nil, err
	}
	var items []itemRow
	if err := r.s.DB(ctx).NewSelect().Model(&items).Where("order_id = ?", id).OrderExpr("position").Scan(ctx); err != nil {
		return nil, err
	}
	return row.toDomain(items), nil
}

func (r *orderRepo) Find(ctx context.Context, id string) (*domain.Order, error) {
	return r.load(ctx, id, false)
}

func (r *orderRepo) Lock(ctx context.Context, id string) (*domain.Order, error) {
	return r.load(ctx, id, true)
}

// Save writes what changes after creation. The items, the totals of the items and the payment token never do.
func (r *orderRepo) Save(ctx context.Context, o *domain.Order) error {
	s := o.Snapshot()
	res, err := r.s.DB(ctx).NewUpdate().Model((*orderRow)(nil)).
		Set("status = ?", string(s.Status)).Set("total_discount = ?", s.TotalDiscount).Set("total_checkout = ?", s.TotalCheckout).
		Set("cancel_reason = ?", nullable(s.CancelReason)).Set("tracking_number = ?", nullable(s.TrackingNumber)).
		Set("version = ?", s.Version).Set("updated_at = ?", s.UpdatedAt).Where("id = ?", s.ID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound()
	}
	return nil
}

func (r *orderRepo) ListByUser(ctx context.Context, userID string, offset, limit int) ([]*domain.Order, int64, error) {
	var rows []orderRow
	n, err := r.s.DB(ctx).NewSelect().Model(&rows).Where("user_id = ?", userID).OrderExpr("created_at DESC, id DESC").
		Offset(int64(offset)).Limit(int64(limit)).ScanAndCount(ctx)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	byOrder := map[string][]itemRow{}
	if len(ids) > 0 {
		var items []itemRow
		if err := r.s.DB(ctx).NewSelect().Model(&items).Where("order_id IN (?)", bun.List(ids)).OrderExpr("order_id, position").Scan(ctx); err != nil {
			return nil, 0, err
		}
		for _, it := range items {
			byOrder[it.OrderID] = append(byOrder[it.OrderID], it)
		}
	}
	out := make([]*domain.Order, len(rows))
	for i := range rows {
		out[i] = rows[i].toDomain(byOrder[rows[i].ID])
	}
	return out, int64(n), nil
}

package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// MaxKeyLength bounds an Idempotency-Key.
const MaxKeyLength = 128

// PlaceInput is a checkout request.
type PlaceInput struct {
	ShopOrders     []ShopOrder
	Shipping       domain.Shipping
	PaymentMethod  string
	PaymentToken   string
	IdempotencyKey string
}

// Placed is the answer: the order was accepted, the saga is running.
type Placed struct {
	OrderID string
	Status  domain.Status
}

// PlaceOrder accepts an order and starts the checkout saga.
type PlaceOrder struct{ d *Deps }

// errClaimLost is returned inside the transaction when another request claimed the same Idempotency-Key first.
var errClaimLost = errors.New("idempotency key claimed by a concurrent request")

// Execute is idempotent per (buyer, Idempotency-Key): the same key with the same request returns the same order, the
// same key with a different request is IDEMPOTENCY_CONFLICT. A new request is re-priced on the server, then the order,
// its saga, the key and the first command (ReserveStock) are stored in ONE transaction, so there is no order without a
// running saga and no saga whose first command was lost.
func (u PlaceOrder) Execute(ctx context.Context, in PlaceInput) (Placed, error) {
	me, err := userCaller(ctx)
	if err != nil {
		return Placed{}, err
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" || len(key) > MaxKeyLength {
		return Placed{}, errs.InvalidArgument(domain.CodeIdempotencyKey, "an Idempotency-Key of 1 to 128 characters is required")
	}
	hash := requestHash(in)

	if rec, err := u.d.Keys.Find(ctx, me.ID, key); err != nil || rec != nil {
		return u.replay(ctx, rec, hash, err)
	}
	rv, err := u.d.review(ctx, in.ShopOrders)
	if err != nil {
		return Placed{}, err
	}
	order, err := u.newOrder(me.ID, in, rv)
	if err != nil {
		return Placed{}, err
	}

	now := u.d.Clock.Now()
	sg, started := domain.StartSaga(order.ID(), order.HasDiscounts(), now, u.d.timeouts())
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		// the key first: if a concurrent request holds it, this waits for it and then loses without writing anything
		claimed, err := u.d.Keys.Claim(ctx, domain.IdempotencyRecord{UserID: me.ID, Key: key, RequestHash: hash, OrderID: order.ID(), CreatedAt: now})
		if err != nil {
			return err
		}
		if !claimed {
			return errClaimLost
		}
		if err := u.d.Orders.Insert(ctx, order); err != nil {
			return err
		}
		if err := u.d.Sagas.Insert(ctx, sg); err != nil {
			return err
		}
		return u.d.send(ctx, order, started.Commands)
	})
	if errors.Is(err, errClaimLost) {
		rec, err := u.d.Keys.Find(ctx, me.ID, key)
		return u.replay(ctx, rec, hash, err)
	}
	if err != nil {
		return Placed{}, err
	}
	u.d.metrics().Started()
	return Placed{OrderID: order.ID(), Status: order.Status()}, nil
}

// replay answers a repeated request from the order the key already created.
func (u PlaceOrder) replay(ctx context.Context, rec *domain.IdempotencyRecord, hash string, findErr error) (Placed, error) {
	if findErr != nil {
		return Placed{}, findErr
	}
	if rec == nil { // claimed by a request that then failed and rolled back: let the client retry
		return Placed{}, errs.Aborted(domain.CodeIdempotencyConflict, "a concurrent request with this Idempotency-Key did not complete, retry")
	}
	if rec.RequestHash != hash {
		return Placed{}, domain.ErrIdempotencyConflict()
	}
	o, err := u.d.Orders.Find(ctx, rec.OrderID)
	if err != nil {
		return Placed{}, err
	}
	return Placed{OrderID: o.ID(), Status: o.Status()}, nil
}

func (u PlaceOrder) newOrder(userID string, in PlaceInput, rv Review) (*domain.Order, error) {
	var items []domain.Item
	for _, s := range rv.Shops {
		items = append(items, s.Items...)
	}
	return domain.NewOrder(domain.NewOrderInput{ID: u.d.IDs.New(), UserID: userID, Items: items, Discounts: discountRefs(in.ShopOrders),
		FeeShip: u.d.FeeShip, Shipping: in.Shipping, PaymentMethod: in.PaymentMethod, PaymentToken: in.PaymentToken}, u.d.Clock.Now())
}

// requestHash identifies a request independently of the order its lists were sent in: same content, same hash.
func requestHash(in PlaceInput) string {
	type item struct {
		P string `json:"p"`
		Q int64  `json:"q"`
	}
	type shop struct {
		S string `json:"s"`
		D string `json:"d"`
		I []item `json:"i"`
	}
	shops := make([]shop, 0, len(in.ShopOrders))
	for _, so := range in.ShopOrders {
		sh := shop{S: so.ShopID, D: strings.ToUpper(strings.TrimSpace(so.DiscountCode))}
		for _, it := range so.Items {
			sh.I = append(sh.I, item{it.ProductID, it.Quantity})
		}
		sort.Slice(sh.I, func(a, b int) bool { return sh.I[a].P < sh.I[b].P })
		shops = append(shops, sh)
	}
	sort.Slice(shops, func(a, b int) bool { return shops[a].S < shops[b].S })
	raw, _ := json.Marshal(struct {
		Shops    []shop          `json:"shops"`
		Shipping domain.Shipping `json:"shipping"`
		Method   string          `json:"method"`
		Token    string          `json:"token"`
	}{shops, in.Shipping, strings.TrimSpace(in.PaymentMethod), in.PaymentToken})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/discount-service/internal/fake"
	transport "github.com/an-vt/ecom/discount-service/internal/transport/kafka"
	"github.com/an-vt/ecom/discount-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/saga"
)

type memGuard struct{ seen map[string]bool }

func (g *memGuard) TryRecord(_ context.Context, c, id string) (bool, error) {
	k := c + "/" + id
	if g.seen[k] {
		return false, nil
	}
	g.seen[k] = true
	return true, nil
}

var now = time.Now()

func TestSagaCommandsAreHandledOnceAndRepliesAreWritten(t *testing.T) {
	w := fake.NewWorld()
	uc := usecase.NewSet(&usecase.Deps{Discounts: w.Discounts(), Usages: w.Usages(), Catalog: &fake.Catalog{}, Tx: w, Outbox: w, Clock: clock.NewFixed(now), IDs: idgen.UUID{}})
	d, err := domain.New(domain.NewInput{ID: idgen.UUID{}.New(), ShopID: "shop1", Code: "SALE10", Name: "n", Type: domain.TypePercent, Value: 10,
		StartDate: now.Add(-time.Hour), EndDate: now.Add(time.Hour), MaxUses: 5}, now)
	require.NoError(t, err)
	require.NoError(t, w.Discounts().Insert(context.Background(), d))
	h := transport.Handler(uc, w, &memGuard{seen: map[string]bool{}})

	apply, err := saga.Envelope("m1", now, saga.ApplyDiscounts{OrderID: "o1", UserID: "alice",
		Discounts: []saga.DiscountRequest{{ShopID: "shop1", Code: "SALE10"}}, Items: []saga.Item{{ProductID: "p", ShopID: "shop1", Quantity: 1, Price: 1000}}})
	require.NoError(t, err)
	require.NoError(t, h(context.Background(), apply))
	assert.ErrorIs(t, h(context.Background(), apply), inbox.ErrDuplicate, "the same message id is never handled twice")
	assert.Equal(t, []string{saga.ReplyDiscountsApplied}, w.Types())

	release, _ := saga.Envelope("m2", now, saga.ReleaseDiscounts{OrderID: "o1"})
	require.NoError(t, h(context.Background(), release))
	assert.Equal(t, []string{saga.ReplyDiscountsApplied, saga.ReplyDiscountsReleased}, w.Types())

	broken, _ := saga.Envelope("m3", now, saga.ApplyDiscounts{OrderID: "o2"})
	broken.Payload = []byte(`"nope"`)
	assert.True(t, errs.IsPermanent(h(context.Background(), broken)), "a malformed command goes to the dead-letter topic, not into a retry loop")
	brokenRelease, _ := saga.Envelope("m4", now, saga.ReleaseDiscounts{OrderID: "o2"})
	brokenRelease.Payload = []byte(`"nope"`)
	assert.True(t, errs.IsPermanent(h(context.Background(), brokenRelease)))

	other, _ := saga.Envelope("m5", now, saga.ProcessPayment{OrderID: "o3"})
	assert.ErrorIs(t, h(context.Background(), other), kafkax.ErrIgnored)
	assert.Equal(t, []string{saga.TopicDiscountCommands}, transport.Topics)
}

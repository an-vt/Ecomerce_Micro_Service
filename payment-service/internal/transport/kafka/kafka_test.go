package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/payment-service/internal/fake"
	transport "github.com/an-vt/ecom/payment-service/internal/transport/kafka"
	"github.com/an-vt/ecom/payment-service/internal/usecase"
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
	prov := &fake.Provider{}
	uc := usecase.NewSet(&usecase.Deps{Payments: w.Payments(), Provider: prov, Tx: w, Outbox: w, Clock: clock.NewFixed(now), IDs: idgen.UUID{}})
	h := transport.Handler(uc, w, &memGuard{seen: map[string]bool{}})

	pay, err := saga.Envelope("m1", now, saga.ProcessPayment{OrderID: "o1", UserID: "alice", Amount: 1000, Method: "card", Token: "tok_ok"})
	require.NoError(t, err)
	require.NoError(t, h(context.Background(), pay))
	assert.ErrorIs(t, h(context.Background(), pay), inbox.ErrDuplicate, "the same message id is never handled twice")
	assert.Equal(t, []string{saga.ReplyPaymentCompleted}, w.Types())
	assert.Len(t, prov.ChargeCalls, 1)

	refund, _ := saga.Envelope("m2", now, saga.RefundPayment{OrderID: "o1"})
	require.NoError(t, h(context.Background(), refund))
	assert.Equal(t, []string{saga.ReplyPaymentCompleted, saga.ReplyPaymentRefunded}, w.Types())

	brokenPay, _ := saga.Envelope("m3", now, saga.ProcessPayment{OrderID: "o2"})
	brokenPay.Payload = []byte(`"nope"`)
	assert.True(t, errs.IsPermanent(h(context.Background(), brokenPay)), "a malformed command goes to the dead-letter topic, not into a retry loop")
	brokenRefund, _ := saga.Envelope("m4", now, saga.RefundPayment{OrderID: "o2"})
	brokenRefund.Payload = []byte(`"nope"`)
	assert.True(t, errs.IsPermanent(h(context.Background(), brokenRefund)))

	other, _ := saga.Envelope("m5", now, saga.ApplyDiscounts{OrderID: "o3"})
	assert.ErrorIs(t, h(context.Background(), other), kafkax.ErrIgnored)
	assert.Equal(t, []string{saga.TopicPaymentCommands}, transport.Topics)
}

package saga_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/saga"
)

func TestEveryMessageHasADistinctTypeAndTheOrderIDAsKey(t *testing.T) {
	msgs := []saga.Message{
		saga.ReserveStock{OrderID: "o"}, saga.ReleaseStock{OrderID: "o"}, saga.CommitStock{OrderID: "o"},
		saga.ApplyDiscounts{OrderID: "o"}, saga.ReleaseDiscounts{OrderID: "o"}, saga.ProcessPayment{OrderID: "o"}, saga.RefundPayment{OrderID: "o"},
		saga.StockReserved{OrderID: "o"}, saga.StockReservationFailed{OrderID: "o"}, saga.StockReleased{OrderID: "o"}, saga.StockCommitted{OrderID: "o"},
		saga.DiscountsApplied{OrderID: "o"}, saga.DiscountsRejected{OrderID: "o"}, saga.DiscountsReleased{OrderID: "o"},
		saga.PaymentCompleted{OrderID: "o"}, saga.PaymentFailed{OrderID: "o"}, saga.PaymentRefunded{OrderID: "o"},
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		assert.False(t, seen[m.MessageType()], "duplicate type %s", m.MessageType())
		seen[m.MessageType()] = true
		assert.Equal(t, "o", m.Order())
		env, err := saga.Envelope("m1", time.Unix(0, 0), m)
		require.NoError(t, err)
		assert.Equal(t, m.MessageType(), env.Type)
		assert.Equal(t, "o", env.AggregateID, "the order id is the Kafka key")
		assert.Equal(t, "o", env.CorrelationID)
	}
	assert.Len(t, seen, 17)
}

func TestMessagesSurviveAJSONRoundTrip(t *testing.T) {
	in := saga.ApplyDiscounts{OrderID: "o1", UserID: "u1",
		Discounts: []saga.DiscountRequest{{ShopID: "s1", Code: "SAVE10"}},
		Items:     []saga.Item{{ProductID: "p1", ShopID: "s1", Quantity: 2, Price: 150000}}}
	env, err := saga.Envelope("m", time.Now(), in)
	require.NoError(t, err)
	raw, err := env.Marshal()
	require.NoError(t, err)
	parsed, err := event.Parse(raw)
	require.NoError(t, err)
	var out saga.ApplyDiscounts
	require.NoError(t, parsed.Decode(&out))
	assert.Equal(t, in, out)
}

func TestTopicsMatchTheKafkaTopics(t *testing.T) {
	assert.Equal(t, "inventory.commands", saga.TopicInventoryCommands)
	assert.Equal(t, "discount.commands", saga.TopicDiscountCommands)
	assert.Equal(t, "payment.commands", saga.TopicPaymentCommands)
	assert.Equal(t, "order.saga.replies", saga.TopicReplies)
}

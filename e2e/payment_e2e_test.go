//go:build e2e

package e2e_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
	"github.com/an-vt/ecom/platform-go/saga"
)

func payOrder(prefix string) string {
	return fmt.Sprintf("e2e-pay-%s-%d", prefix, time.Now().UnixNano())
}

func pay(orderID, token string) saga.ProcessPayment {
	return saga.ProcessPayment{OrderID: orderID, UserID: "e2e-user", Amount: 150000, Method: "card", Token: token}
}

// payment-service has no REST or gRPC API: it is driven by payment.commands and answers on order.saga.replies.
// order-service does not exist yet in this phase, so the test plays its part.
func TestPaymentTokens(t *testing.T) {
	ok, declined, slow := payOrder("ok"), payOrder("fail"), payOrder("slow")

	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, pay(ok, "tok_ok"))
	var completed saga.PaymentCompleted
	e2ekit.WaitForReply(t, ok, saga.ReplyPaymentCompleted, &completed)
	assert.NotEmpty(t, completed.PaymentID)

	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, pay(declined, "tok_fail"))
	var failed saga.PaymentFailed
	e2ekit.WaitForReply(t, declined, saga.ReplyPaymentFailed, &failed)
	assert.Equal(t, saga.ReasonCardDeclined, failed.Reason)

	// tok_timeout: the provider hangs, the deadline cuts it off (3 s), the step fails so the saga can compensate
	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, pay(slow, "tok_timeout"))
	var unavailable saga.PaymentFailed
	e2ekit.WaitForReply(t, slow, saga.ReplyPaymentFailed, &unavailable)
	assert.Equal(t, saga.ReasonProviderUnavailable, unavailable.Reason)
}

func TestARepeatedChargeIsAnsweredTheSameWay(t *testing.T) {
	order := payOrder("dup")
	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, pay(order, "tok_ok"))
	e2ekit.WaitForReply(t, order, saga.ReplyPaymentCompleted, nil)
	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, pay(order, "tok_ok")) // a new message for the same order, as a saga retry would send
	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, pay(order, "tok_fail"))

	require.Eventually(t, func() bool { return len(e2ekit.Replies(t, order, 2*time.Second)) >= 3 }, 20*time.Second, 200*time.Millisecond)
	replies := e2ekit.Replies(t, order, 2*time.Second)
	require.Len(t, replies, 3)
	var first saga.PaymentCompleted
	require.NoError(t, replies[0].Decode(&first))
	for _, r := range replies {
		assert.Equal(t, saga.ReplyPaymentCompleted, r.Type, "even a copy carrying a bad token gets the recorded outcome: the order was paid once")
		var c saga.PaymentCompleted
		require.NoError(t, r.Decode(&c))
		assert.Equal(t, first.PaymentID, c.PaymentID)
	}
}

func TestRefundAfterAChargeAndBeforeOne(t *testing.T) {
	paid := payOrder("paid")
	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, pay(paid, "tok_ok"))
	e2ekit.WaitForReply(t, paid, saga.ReplyPaymentCompleted, nil)
	for i := 0; i < 2; i++ { // compensations are re-sent until acknowledged
		e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, saga.RefundPayment{OrderID: paid})
	}
	e2ekit.WaitForReply(t, paid, saga.ReplyPaymentRefunded, nil)

	// the compensation overtakes the charge (the saga gave up on the step): the late charge must not take money
	early := payOrder("early")
	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, saga.RefundPayment{OrderID: early})
	e2ekit.WaitForReply(t, early, saga.ReplyPaymentRefunded, nil)
	e2ekit.SendSagaCommand(t, saga.TopicPaymentCommands, pay(early, "tok_ok"))
	var refused saga.PaymentFailed
	e2ekit.WaitForReply(t, early, saga.ReplyPaymentFailed, &refused)
	assert.Equal(t, saga.ReasonStepTimeout, refused.Reason)
}

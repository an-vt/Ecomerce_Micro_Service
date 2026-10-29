package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/notification-service/internal/fake"
	transport "github.com/an-vt/ecom/notification-service/internal/transport/kafka"
	"github.com/an-vt/ecom/notification-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/kafkax"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T) (kafkax.Handler, *fake.World, *fake.Sender) {
	t.Helper()
	w, s := fake.NewWorld(), &fake.Sender{}
	uc := usecase.NewSet(&usecase.Deps{Notifications: w.Notifications(), Templates: w.Templates(), Emails: w.Emails(), Sender: s, Clock: clock.NewFixed(now), IDs: idgen.ULID{}})
	_, err := uc.EnsureTemplates.Execute(context.Background())
	require.NoError(t, err)
	return transport.Handler(uc), w, s
}

func env(t *testing.T, id, typ string, payload any) event.Envelope {
	t.Helper()
	e, err := event.NewEnvelope(id, typ, id, "", now, payload)
	require.NoError(t, err)
	return e
}

func TestEveryTopicIsRoutedToItsUseCase(t *testing.T) {
	h, w, smtp := setup(t)
	ctx := context.Background()

	// acceptance #2: the verification event becomes an email with the link
	require.NoError(t, h(ctx, env(t, "m1", "auth.email-verification.requested", map[string]any{"otpId": "o", "email": "an@example.com",
		"link": "http://localhost:8088/v1/api/user/welcome_back?token=T", "expiresAt": now.Add(10 * time.Minute)})))
	require.Equal(t, 1, smtp.Count())
	assert.Contains(t, smtp.Sent[0].HTML, "welcome_back?token=T")

	require.NoError(t, h(ctx, env(t, "m2", "auth.user.registered", map[string]any{"userId": "u1", "email": "an@example.com"})))
	assert.Equal(t, 2, smtp.Count(), "the welcome email")

	require.NoError(t, h(ctx, env(t, "m3", "catalog.product.created", map[string]any{"productId": "p1", "shopId": "s1", "name": "Shirt", "price": 100, "quantity": 5, "type": "Clothing"})))
	require.NoError(t, h(ctx, env(t, "m4", "discount.discount.created", map[string]any{"discountId": "d1", "shopId": "s1", "code": "SALE10", "name": "Ten", "type": "percent", "value": 10})))
	require.NoError(t, h(ctx, env(t, "m5", "order.order.confirmed", map[string]any{"orderId": "o1", "userId": "alice", "totalCheckout": 5000, "items": []map[string]any{{"productId": "p1", "quantity": 1}}})))
	require.NoError(t, h(ctx, env(t, "m6", "order.order.cancelled", map[string]any{"orderId": "o2", "userId": "alice", "reason": "CARD_DECLINED"})))

	types := map[domain.Type]bool{}
	for _, n := range w.All() {
		types[n.Type] = true
	}
	assert.Equal(t, map[domain.Type]bool{domain.TypeNewProduct: true, domain.TypePromotion: true, domain.TypeOrderConfirmed: true, domain.TypeOrderCancelled: true}, types)
}

func TestRedeliveryChangesNothing(t *testing.T) {
	h, w, smtp := setup(t)
	e1 := env(t, "m1", "auth.email-verification.requested", map[string]any{"email": "an@example.com", "link": "http://x/?t=1", "expiresAt": now.Add(time.Hour)})
	e2 := env(t, "m2", "order.order.confirmed", map[string]any{"orderId": "o1", "userId": "alice", "totalCheckout": 1})
	for i := 0; i < 3; i++ {
		require.NoError(t, h(context.Background(), e1))
		require.NoError(t, h(context.Background(), e2))
	}
	assert.Equal(t, 1, smtp.Count())
	assert.Len(t, w.All(), 1)
}

func TestMalformedAndForeignMessages(t *testing.T) {
	h, _, _ := setup(t)
	for _, typ := range []string{"auth.email-verification.requested", "auth.user.registered", "catalog.product.created", "discount.discount.created", "order.order.confirmed", "order.order.cancelled"} {
		e := env(t, "bad-"+typ, typ, "just a string")
		assert.True(t, errs.IsPermanent(h(context.Background(), e)), typ+": a malformed message goes to the dead-letter topic, not into a retry loop")
	}
	assert.ErrorIs(t, h(context.Background(), env(t, "x", "something.else", map[string]any{})), kafkax.ErrIgnored)
	assert.Len(t, transport.Topics, 6)
}

func TestSMTPFailureIsRetryable(t *testing.T) {
	h, _, smtp := setup(t)
	smtp.Err = assert.AnError
	err := h(context.Background(), env(t, "m1", "auth.user.registered", map[string]any{"email": "an@example.com"}))
	require.Error(t, err)
	assert.False(t, errs.IsPermanent(err), "the runner retries it with backoff and then dead-letters it")
}

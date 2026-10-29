package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/catalog-service/internal/fake"
	transport "github.com/an-vt/ecom/catalog-service/internal/transport/kafka"
	"github.com/an-vt/ecom/catalog-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/inbox"
)

type memGuard struct{ seen map[string]bool }

func (g *memGuard) TryRecord(_ context.Context, consumer, id string) (bool, error) {
	k := consumer + "/" + id
	if g.seen[k] {
		return false, nil
	}
	g.seen[k] = true
	return true, nil
}

func setup() (*fake.World, func(event.Envelope) error) {
	w := fake.NewWorld()
	uc := usecase.NewSet(&usecase.Deps{Products: w.Products(), Spus: w.Spus(), Comments: w.Comments(), Shops: w.Shops(),
		Types: domain.DefaultTypes(), Tx: w, Outbox: w, SkuCache: fake.NewCache(), Clock: clock.NewFixed(time.Now()), IDs: idgen.ULID{}})
	h := transport.Handler(uc, w, &memGuard{seen: map[string]bool{}})
	return w, func(e event.Envelope) error { return h(context.Background(), e) }
}

func envelope(t *testing.T, id, typ string, payload any) event.Envelope {
	t.Helper()
	e, err := event.NewEnvelope(id, typ, "shop-1", "", time.Now(), payload)
	require.NoError(t, err)
	return e
}

func TestShopRegisteredBuildsTheProjectionOnce(t *testing.T) {
	w, handle := setup()
	e := envelope(t, "m1", "auth.shop.registered", map[string]string{"shopId": "shop-1", "name": "Acme", "email": "a@acme.com"})
	require.NoError(t, handle(e))
	got, _ := w.Shops().FindByIDs(context.Background(), []string{"shop-1"})
	assert.Equal(t, "Acme", got["shop-1"].Name)

	assert.ErrorIs(t, handle(e), inbox.ErrDuplicate, "a redelivered message is recognised")
	renamed := envelope(t, "m2", "auth.shop.registered", map[string]string{"shopId": "shop-1", "name": "Acme 2", "email": "a@acme.com"})
	require.NoError(t, handle(renamed))
	got, _ = w.Shops().FindByIDs(context.Background(), []string{"shop-1"})
	assert.Equal(t, "Acme 2", got["shop-1"].Name)
}

func TestBadPayloadsAndOtherTypes(t *testing.T) {
	_, handle := setup()
	bad := envelope(t, "m3", "auth.shop.registered", "not an object")
	err := handle(bad)
	require.Error(t, err)
	assert.True(t, errs.IsPermanent(err), "an undecodable payload goes to the DLQ, not into a retry loop")

	noID := envelope(t, "m4", "auth.shop.registered", map[string]string{"name": "x"})
	err = handle(noID)
	require.Error(t, err)
	assert.True(t, errs.IsPermanent(err))

	assert.Error(t, handle(envelope(t, "m5", "something.else", map[string]string{})), "unknown types are skipped by the runner via ErrIgnored")
	assert.Equal(t, []string{"auth.shop.registered"}, transport.Topics)
}

package inbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/inbox"
)

type memGuard struct {
	seen map[string]bool
	err  error
}

func (g *memGuard) TryRecord(_ context.Context, consumer, id string) (bool, error) {
	if g.err != nil {
		return false, g.err
	}
	k := consumer + "/" + id
	if g.seen[k] {
		return false, nil
	}
	g.seen[k] = true
	return true, nil
}

type passTx struct{ calls int }

func (p *passTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	p.calls++
	return fn(ctx)
}

func TestHandleRunsOncePerConsumerAndMessage(t *testing.T) {
	g, tx := &memGuard{seen: map[string]bool{}}, &passTx{}
	runs := 0
	fn := func(context.Context) error { runs++; return nil }

	require.NoError(t, inbox.Handle(context.Background(), tx, g, "inventory", "m1", fn))
	err := inbox.Handle(context.Background(), tx, g, "inventory", "m1", fn)
	assert.ErrorIs(t, err, inbox.ErrDuplicate)
	assert.Equal(t, 1, runs)

	require.NoError(t, inbox.Handle(context.Background(), tx, g, "notification", "m1", fn), "another consumer has its own record")
	require.NoError(t, inbox.Handle(context.Background(), tx, g, "inventory", "m2", fn))
	assert.Equal(t, 3, runs)
	assert.Equal(t, 4, tx.calls)
}

func TestHandlePropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	tx := &passTx{}
	err := inbox.Handle(context.Background(), tx, &memGuard{seen: map[string]bool{}}, "c", "m", func(context.Context) error { return boom })
	assert.ErrorIs(t, err, boom)

	guardErr := errors.New("db down")
	called := false
	err = inbox.Handle(context.Background(), tx, &memGuard{err: guardErr}, "c", "m", func(context.Context) error { called = true; return nil })
	assert.ErrorIs(t, err, guardErr)
	assert.False(t, called)
}

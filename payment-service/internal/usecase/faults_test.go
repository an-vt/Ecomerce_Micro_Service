package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/payment-service/internal/client/provider"
	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/payment-service/internal/fake"
	"github.com/an-vt/ecom/payment-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/saga"
)

var errDB = errors.New("database down")

// faulty wraps the fake repository and fails the operations it is told to.
type faulty struct {
	domain.PaymentRepository
	insert, lock, save error
	hideFirstLock      bool // the first LockByOrder says "not found" although the row exists: a concurrent insert won the race
}

func (f *faulty) Insert(ctx context.Context, p *domain.Payment) (bool, error) {
	if f.insert != nil {
		return false, f.insert
	}
	return f.PaymentRepository.Insert(ctx, p)
}

func (f *faulty) LockByOrder(ctx context.Context, id string) (*domain.Payment, error) {
	if f.lock != nil {
		return nil, f.lock
	}
	if f.hideFirstLock {
		f.hideFirstLock = false
		return nil, domain.ErrNotFound()
	}
	return f.PaymentRepository.LockByOrder(ctx, id)
}

func (f *faulty) Save(ctx context.Context, p *domain.Payment) error {
	if f.save != nil {
		return f.save
	}
	return f.PaymentRepository.Save(ctx, p)
}

func rigWith(f *faulty) (*usecase.Set, *fake.World, *fake.Provider) {
	w := fake.NewWorld()
	f.PaymentRepository = w.Payments()
	p := &fake.Provider{}
	return usecase.NewSet(&usecase.Deps{Payments: f, Provider: p, Tx: w, Outbox: w, Clock: clock.NewFixed(now), IDs: idgen.UUID{}}), w, p
}

func TestStorageFailuresMakeTheHandlerFailSoTheMessageIsRetried(t *testing.T) {
	ctx := context.Background()
	t.Run("insert", func(t *testing.T) {
		uc, w, p := rigWith(&faulty{insert: errDB})
		require.ErrorIs(t, uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)), errDB)
		assert.Empty(t, w.Types())
		assert.Empty(t, p.ChargeCalls, "no charge without a recorded payment")
	})
	t.Run("save after the charge", func(t *testing.T) {
		uc, w, _ := rigWith(&faulty{save: errDB})
		require.ErrorIs(t, uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)), errDB)
		assert.Empty(t, w.Types())
		_, ok := w.Payment("o1")
		assert.False(t, ok, "the transaction rolled back")
	})
	t.Run("lock of an existing payment", func(t *testing.T) {
		f := &faulty{}
		uc, _, _ := rigWith(f)
		require.NoError(t, uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
		f.lock = errDB
		require.ErrorIs(t, uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)), errDB)
		require.ErrorIs(t, uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}), errDB)
	})
	t.Run("refund: tombstone insert", func(t *testing.T) {
		uc, _, _ := rigWith(&faulty{insert: errDB})
		require.ErrorIs(t, uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}), errDB)
	})
	t.Run("refund: save", func(t *testing.T) {
		f := &faulty{}
		uc, _, _ := rigWith(f)
		require.NoError(t, uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
		f.save = errDB
		require.ErrorIs(t, uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}), errDB)
	})
}

// The charge row was inserted by a concurrent transaction between our lookup and our tombstone insert: the refund
// then undoes that charge instead of leaving it standing.
func TestRefundThatLosesTheRaceUndoesTheChargeThatWon(t *testing.T) {
	ctx := context.Background()
	f := &faulty{}
	uc, w, p := rigWith(f)
	require.NoError(t, uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))

	f.hideFirstLock = true
	require.NoError(t, uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}))
	s, _ := w.Payment("o1")
	assert.Equal(t, domain.StatusRefunded, s.Status)
	assert.Len(t, p.RefundCalls, 1)
}

// A PENDING row is never visible between transactions today; if one ever is (an older version, manual repair), a
// repeated command finishes the charge instead of getting stuck.
func TestAPendingPaymentIsChargedByTheNextCommand(t *testing.T) {
	ctx := context.Background()
	f := &faulty{}
	uc, w, p := rigWith(f)
	pending, err := domain.New(domain.NewInput{ID: "p0", OrderID: "o1", UserID: "alice", Amount: 150000, Currency: "VND", Method: "card"}, now)
	require.NoError(t, err)
	_, err = w.Payments().Insert(ctx, pending)
	require.NoError(t, err)

	require.NoError(t, uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
	s, _ := w.Payment("o1")
	assert.Equal(t, domain.StatusSucceeded, s.Status)
	assert.Equal(t, "p0", s.ID, "the existing record was completed, no second one")
	assert.Len(t, p.ChargeCalls, 1)

	// ...and a refund of a payment stuck in PENDING cancels it
	stuck, _ := domain.New(domain.NewInput{ID: "p1", OrderID: "o2", UserID: "alice", Amount: 1, Currency: "VND", Method: "card"}, now)
	_, _ = w.Payments().Insert(ctx, stuck)
	require.NoError(t, uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o2"}))
	s, _ = w.Payment("o2")
	assert.Equal(t, domain.StatusCancelled, s.Status)
}

func TestTheCurrencyDefaultsToVND(t *testing.T) {
	uc, w, _ := rigWith(&faulty{})
	require.NoError(t, uc.ProcessPayment.Execute(context.Background(), charge("o1", provider.TokenOK)))
	s, _ := w.Payment("o1")
	assert.Equal(t, "VND", s.Currency)
}

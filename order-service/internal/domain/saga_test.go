package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/saga"
)

var timeouts = domain.Timeouts{Step: 30 * time.Second, Compensation: 20 * time.Second}

// run drives a saga and its order together, the way the use case does: every result's order steps must be accepted by
// the order's status guard, otherwise the state machine and the aggregate disagree.
type run struct {
	t     *testing.T
	saga  *domain.Saga
	order *domain.Order
	now   time.Time
	cmds  []string // every command sent so far
}

func start(t *testing.T, discounts bool) *run {
	t.Helper()
	in := input()
	if !discounts {
		in.Discounts = nil
	}
	o, err := domain.NewOrder(in, now)
	require.NoError(t, err)
	s, res := domain.StartSaga(o.ID(), o.HasDiscounts(), now, timeouts)
	r := &run{t: t, saga: s, order: o, now: now}
	assert.Equal(t, []string{saga.CmdReserveStock}, res.Commands)
	assert.Equal(t, now.Add(timeouts.Step), s.Snapshot().StepDeadline)
	r.record(res)
	return r
}

func (r *run) record(res domain.Result) domain.Result {
	r.t.Helper()
	r.cmds = append(r.cmds, res.Commands...)
	if res.TotalDiscount != nil {
		require.NoError(r.t, r.order.ApplyDiscount(*res.TotalDiscount, r.now))
	}
	for _, step := range res.Orders {
		if step.To == domain.StatusCancelling {
			require.NoError(r.t, r.order.StartCancelling(step.Reason, r.now), "order step %v", step)
			continue
		}
		require.NoError(r.t, r.order.Advance(step.To, r.now), "order step %v", step)
	}
	return res
}

func (r *run) reply(m saga.Message) domain.Result {
	r.t.Helper()
	r.now = r.now.Add(time.Second)
	return r.record(r.saga.OnReply(m, r.now, timeouts))
}

func (r *run) timeout() domain.Result {
	r.t.Helper()
	r.now = r.saga.Snapshot().StepDeadline.Add(time.Second)
	return r.record(r.saga.OnTimeout(r.now, timeouts))
}

const oid = "o1"

func (r *run) toApplyingDiscount() { r.reply(saga.StockReserved{OrderID: oid}) }

func (r *run) toProcessingPayment(discounts bool) {
	r.reply(saga.StockReserved{OrderID: oid})
	if discounts {
		r.reply(saga.DiscountsApplied{OrderID: oid, TotalDiscount: 25000})
	}
}

func TestHappyPathWithADiscount(t *testing.T) {
	r := start(t, true)

	res := r.reply(saga.StockReserved{OrderID: oid})
	assert.Equal(t, []string{saga.CmdApplyDiscounts}, res.Commands)
	assert.Equal(t, []domain.OrderStep{{To: domain.StatusStockReserved}}, res.Orders)
	assert.Equal(t, domain.StateApplyingDiscount, r.saga.State())

	res = r.reply(saga.DiscountsApplied{OrderID: oid, TotalDiscount: 25000})
	assert.Equal(t, []string{saga.CmdProcessPayment}, res.Commands)
	assert.Equal(t, []domain.OrderStep{{To: domain.StatusDiscountApplied}, {To: domain.StatusPaymentProcessing}}, res.Orders)
	require.NotNil(t, res.TotalDiscount)
	assert.EqualValues(t, 25000, *res.TotalDiscount, "the amount to charge comes from the reply, not from the review")
	assert.EqualValues(t, 230000, r.order.AmountDue())

	res = r.reply(saga.PaymentCompleted{OrderID: oid, PaymentID: "pay-1"})
	assert.Equal(t, []string{saga.CmdCommitStock}, res.Commands)
	assert.Equal(t, domain.StateCommittingStock, r.saga.State())
	assert.Equal(t, "pay-1", r.saga.PaymentID())

	res = r.reply(saga.StockCommitted{OrderID: oid})
	assert.Empty(t, res.Commands)
	assert.Equal(t, []domain.OrderStep{{To: domain.StatusConfirmed}}, res.Orders)
	assert.Equal(t, domain.StateCompleted, res.Finished)
	assert.Equal(t, domain.StatusConfirmed, r.order.Status())
	assert.Equal(t, []string{saga.CmdReserveStock, saga.CmdApplyDiscounts, saga.CmdProcessPayment, saga.CmdCommitStock}, r.cmds)
}

func TestHappyPathWithoutADiscountSkipsThatStep(t *testing.T) {
	r := start(t, false)
	res := r.reply(saga.StockReserved{OrderID: oid})
	assert.Equal(t, []string{saga.CmdProcessPayment}, res.Commands)
	assert.Equal(t, []domain.OrderStep{{To: domain.StatusStockReserved}, {To: domain.StatusPaymentProcessing}}, res.Orders)
	assert.Equal(t, domain.StateProcessingPay, r.saga.State())
	r.reply(saga.PaymentCompleted{OrderID: oid, PaymentID: "p"})
	res = r.reply(saga.StockCommitted{OrderID: oid})
	assert.Equal(t, domain.StateCompleted, res.Finished)
	assert.Equal(t, domain.StatusConfirmed, r.order.Status())
}

// Acceptance #1: every row of docs/06 §4, in the order the commands are sent (most recent step first).
func TestCompensationMatrix(t *testing.T) {
	tests := []struct {
		name      string
		discounts bool
		reach     func(*run)
		trigger   func(*run) domain.Result
		undo      []string
		reason    string
	}{
		{"StockReservationFailed: nothing to undo", true, func(*run) {},
			func(r *run) domain.Result {
				return r.reply(saga.StockReservationFailed{OrderID: oid, Reason: saga.ReasonInsufficientStock, ProductID: "p1"})
			}, nil, saga.ReasonInsufficientStock},
		{"step 1 timeout", true, func(*run) {},
			func(r *run) domain.Result { return r.timeout() },
			[]string{saga.CmdReleaseStock}, saga.ReasonStepTimeout},
		{"DiscountsRejected", true, func(r *run) { r.toApplyingDiscount() },
			func(r *run) domain.Result {
				return r.reply(saga.DiscountsRejected{OrderID: oid, Reason: "DISCOUNT_USED_UP", Code: "SALE10"})
			},
			[]string{saga.CmdReleaseStock}, "DISCOUNT_USED_UP"},
		{"step 2 timeout", true, func(r *run) { r.toApplyingDiscount() },
			func(r *run) domain.Result { return r.timeout() },
			[]string{saga.CmdReleaseDiscount, saga.CmdReleaseStock}, saga.ReasonStepTimeout},
		{"PaymentFailed after discounts", true, func(r *run) { r.toProcessingPayment(true) },
			func(r *run) domain.Result {
				return r.reply(saga.PaymentFailed{OrderID: oid, Reason: saga.ReasonCardDeclined})
			},
			[]string{saga.CmdReleaseDiscount, saga.CmdReleaseStock}, saga.ReasonCardDeclined},
		{"PaymentFailed without discounts", false, func(r *run) { r.toProcessingPayment(false) },
			func(r *run) domain.Result {
				return r.reply(saga.PaymentFailed{OrderID: oid, Reason: saga.ReasonProviderUnavailable})
			},
			[]string{saga.CmdReleaseStock}, saga.ReasonProviderUnavailable},
		{"step 3 timeout after discounts", true, func(r *run) { r.toProcessingPayment(true) },
			func(r *run) domain.Result { return r.timeout() },
			[]string{saga.CmdRefundPayment, saga.CmdReleaseDiscount, saga.CmdReleaseStock}, saga.ReasonStepTimeout},
		{"step 3 timeout without discounts", false, func(r *run) { r.toProcessingPayment(false) },
			func(r *run) domain.Result { return r.timeout() },
			[]string{saga.CmdRefundPayment, saga.CmdReleaseStock}, saga.ReasonStepTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := start(t, tc.discounts)
			tc.reach(r)
			res := tc.trigger(r)

			assert.Equal(t, tc.undo, res.Commands)
			assert.Equal(t, tc.reason, r.order.Snapshot().CancelReason)
			if len(tc.undo) == 0 {
				assert.Equal(t, domain.StateFailed, r.saga.State(), "nothing to undo: the saga fails on the spot")
				assert.Equal(t, domain.StateFailed, res.Finished)
				assert.Equal(t, domain.StatusCancelled, r.order.Status())
				return
			}
			assert.Equal(t, domain.StateCompensating, r.saga.State())
			assert.True(t, res.Compensating)
			assert.Equal(t, domain.StatusCancelling, r.order.Status())
			assert.Equal(t, r.now.Add(timeouts.Compensation), r.saga.Snapshot().StepDeadline)

			// acknowledging every undo ends the saga
			acks := map[string]saga.Message{
				saga.CmdReleaseStock:    saga.StockReleased{OrderID: oid},
				saga.CmdReleaseDiscount: saga.DiscountsReleased{OrderID: oid},
				saga.CmdRefundPayment:   saga.PaymentRefunded{OrderID: oid},
			}
			for i, cmd := range tc.undo {
				res = r.reply(acks[cmd])
				if i < len(tc.undo)-1 {
					assert.Equal(t, domain.StateCompensating, r.saga.State(), "still waiting for the rest")
					assert.Empty(t, res.Orders)
				}
			}
			assert.Equal(t, domain.StateFailed, r.saga.State())
			assert.Equal(t, domain.StateFailed, res.Finished)
			assert.Equal(t, []domain.OrderStep{{To: domain.StatusCancelled}}, res.Orders)
			assert.Equal(t, domain.StatusCancelled, r.order.Status())
			assert.Equal(t, tc.reason, r.saga.Snapshot().Reason)
		})
	}
}

// Acceptance #3 (pure part): replies that do not fit are ignored and change nothing.
func TestStaleRepliesAreIgnored(t *testing.T) {
	t.Run("a reply for a step already given up on", func(t *testing.T) {
		r := start(t, true)
		r.timeout() // step 1 timed out: ReleaseStock sent
		before := r.saga.Snapshot()
		res := r.saga.OnReply(saga.StockReserved{OrderID: oid}, r.now, timeouts) // the reservation answer arrives late
		assert.True(t, res.Stale)
		assert.Empty(t, res.Commands)
		assert.Empty(t, res.Orders)
		assert.Equal(t, before, r.saga.Snapshot(), "and the saga is untouched")
	})
	t.Run("a duplicate of a reply already handled", func(t *testing.T) {
		r := start(t, true)
		r.toApplyingDiscount()
		assert.True(t, r.saga.OnReply(saga.StockReserved{OrderID: oid}, r.now, timeouts).Stale)
	})
	t.Run("a reply for a step that has not run yet", func(t *testing.T) {
		r := start(t, true)
		assert.True(t, r.saga.OnReply(saga.PaymentCompleted{OrderID: oid}, r.now, timeouts).Stale)
		assert.True(t, r.saga.OnReply(saga.StockCommitted{OrderID: oid}, r.now, timeouts).Stale)
		assert.Equal(t, domain.StateReservingStock, r.saga.State())
	})
	t.Run("a failure after the pivot cannot undo the payment", func(t *testing.T) {
		r := start(t, false)
		r.toProcessingPayment(false)
		r.reply(saga.PaymentCompleted{OrderID: oid, PaymentID: "p"})
		assert.True(t, r.saga.OnReply(saga.PaymentFailed{OrderID: oid, Reason: "X"}, r.now, timeouts).Stale)
		assert.Equal(t, domain.StateCommittingStock, r.saga.State())
	})
	t.Run("while compensating", func(t *testing.T) {
		r := start(t, true)
		r.toProcessingPayment(true)
		r.timeout() // Refund, ReleaseDiscounts, ReleaseStock sent
		assert.True(t, r.saga.OnReply(saga.PaymentCompleted{OrderID: oid}, r.now, timeouts).Stale, "the late payment will be refunded")
		r.reply(saga.StockReleased{OrderID: oid})
		assert.True(t, r.saga.OnReply(saga.StockReleased{OrderID: oid}, r.now, timeouts).Stale, "an acknowledgement counts once")
		assert.Equal(t, domain.StateCompensating, r.saga.State())
	})
	t.Run("after the end", func(t *testing.T) {
		r := start(t, false)
		r.toProcessingPayment(false)
		r.reply(saga.PaymentCompleted{OrderID: oid, PaymentID: "p"})
		r.reply(saga.StockCommitted{OrderID: oid})
		assert.True(t, r.saga.OnReply(saga.StockCommitted{OrderID: oid}, r.now, timeouts).Stale)
		assert.True(t, r.saga.OnTimeout(r.now.Add(time.Hour), timeouts).Stale, "a finished saga has no deadline")
	})
}

func TestTimeoutsAfterThePivotOnlyMoveForward(t *testing.T) {
	r := start(t, false)
	r.toProcessingPayment(false)
	r.reply(saga.PaymentCompleted{OrderID: oid, PaymentID: "p"})
	deadline := r.saga.Snapshot().StepDeadline

	res := r.timeout()
	assert.Equal(t, []string{saga.CmdCommitStock}, res.Commands, "CommitStock is sent again")
	assert.True(t, res.Resent)
	assert.Equal(t, domain.StateCommittingStock, res.TimedOut)
	assert.Empty(t, res.Orders, "the order stays as it is: it is never cancelled after a successful payment")
	assert.Equal(t, domain.StateCommittingStock, r.saga.State())
	assert.True(t, r.saga.Snapshot().StepDeadline.After(deadline))
	assert.Equal(t, 1, r.saga.Snapshot().Attempts)

	res = r.timeout()
	assert.Equal(t, []string{saga.CmdCommitStock}, res.Commands)
	assert.Equal(t, 2, r.saga.Snapshot().Attempts)
}

func TestCompensationsAreResentUntilAcknowledged(t *testing.T) {
	r := start(t, true)
	r.toProcessingPayment(true)
	res := r.timeout()
	assert.Equal(t, []string{saga.CmdRefundPayment, saga.CmdReleaseDiscount, saga.CmdReleaseStock}, res.Commands)

	r.reply(saga.PaymentRefunded{OrderID: oid})
	res = r.timeout()
	assert.Equal(t, []string{saga.CmdReleaseDiscount, saga.CmdReleaseStock}, res.Commands, "only what is still pending")
	assert.True(t, res.Resent)
	assert.Equal(t, domain.StateCompensating, res.TimedOut)
	assert.Equal(t, r.now.Add(timeouts.Compensation), r.saga.Snapshot().StepDeadline)
	assert.Equal(t, domain.StateCompensating, r.saga.State(), "it never gives up")
	assert.Equal(t, 2, r.saga.Snapshot().Attempts, "the count drives the alert after 3 re-sends")

	r.reply(saga.DiscountsReleased{OrderID: oid})
	r.reply(saga.StockReleased{OrderID: oid})
	assert.Equal(t, domain.StatusCancelled, r.order.Status())
}

func TestSagaSnapshotRoundTripAndIsolation(t *testing.T) {
	r := start(t, true)
	r.toProcessingPayment(true)
	r.timeout()
	s := r.saga.Snapshot()
	assert.Equal(t, s, domain.RestoreSaga(s).Snapshot())
	s.Pending[0] = "hacked"
	assert.Equal(t, saga.CmdRefundPayment, r.saga.Snapshot().Pending[0], "the snapshot does not alias the saga")
	assert.Equal(t, oid, r.saga.ID())
	assert.EqualValues(t, 25000, r.saga.Snapshot().DiscountTotal)
	assert.True(t, domain.StateCompleted.Terminal())
	assert.True(t, domain.StateFailed.Terminal())
	assert.False(t, domain.StateCompensating.Terminal())
}

// The version moves with every change, which is what the sweeper and operators use to see progress.
func TestEveryChangeBumpsTheVersion(t *testing.T) {
	r := start(t, false)
	v := r.saga.Snapshot().Version
	r.reply(saga.StockReserved{OrderID: oid})
	assert.Greater(t, r.saga.Snapshot().Version, v)
	v = r.saga.Snapshot().Version
	r.saga.OnReply(saga.StockReserved{OrderID: oid}, r.now, timeouts) // stale
	assert.Equal(t, v, r.saga.Snapshot().Version, "a stale reply is not a change")
}

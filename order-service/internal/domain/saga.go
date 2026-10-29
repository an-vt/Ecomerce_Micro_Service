package domain

import (
	"time"

	"github.com/an-vt/ecom/platform-go/saga"
)

// SagaState is where the checkout is (docs/06 §2.1).
type SagaState string

const (
	StateReservingStock   SagaState = "RESERVING_STOCK"
	StateApplyingDiscount SagaState = "APPLYING_DISCOUNT"
	StateProcessingPay    SagaState = "PROCESSING_PAYMENT"
	StateCommittingStock  SagaState = "COMMITTING_STOCK"
	StateCompleted        SagaState = "COMPLETED"
	StateCompensating     SagaState = "COMPENSATING"
	StateFailed           SagaState = "FAILED"
)

// Terminal reports whether the saga is over.
func (s SagaState) Terminal() bool { return s == StateCompleted || s == StateFailed }

// Timeouts are how long a step may take before the saga acts (SAGA_STEP_TIMEOUT_MS, SAGA_COMPENSATION_RETRY_MS).
type Timeouts struct {
	Step         time.Duration
	Compensation time.Duration
}

// OrderStep is a transition to apply to the order, in sequence.
type OrderStep struct {
	To     Status
	Reason string // only for CANCELLING
}

// Result is what the saga decided. It contains no I/O: the use case sends the commands, applies the order steps and
// publishes the events.
type Result struct {
	Commands      []string    // saga.Cmd* kinds to send, in order (the use case builds the payloads from the order)
	Orders        []OrderStep // order transitions to apply, in order
	TotalDiscount *int64      // set when DiscountsApplied arrived
	Stale         bool        // the message did not fit the current state and was ignored
	TimedOut      SagaState   // the forward state whose deadline passed (for metrics)
	Compensating  bool        // the saga entered COMPENSATING with this result
	Finished      SagaState   // COMPLETED or FAILED when this result ended the saga
	Resent        bool        // commands re-sent by a timeout (CommitStock, compensations)
}

// Saga is the pure state machine of one checkout. State changes happen only in OnReply and OnTimeout.
type Saga struct{ s SagaSnapshot }

// SagaSnapshot is the persistable state.
type SagaSnapshot struct {
	ID            string // = order id
	State         SagaState
	HasDiscounts  bool
	StepDeadline  time.Time
	PaymentID     string
	DiscountTotal int64
	Pending       []string // compensation commands not acknowledged yet
	Reason        string   // why it is compensating / failed
	Attempts      int      // times a command was re-sent after a timeout
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// StartSaga begins a checkout: the first command is ReserveStock.
func StartSaga(orderID string, hasDiscounts bool, now time.Time, t Timeouts) (*Saga, Result) {
	s := &Saga{s: SagaSnapshot{ID: orderID, State: StateReservingStock, HasDiscounts: hasDiscounts, StepDeadline: now.Add(t.Step), CreatedAt: now, UpdatedAt: now}}
	return s, Result{Commands: []string{saga.CmdReserveStock}}
}

// RestoreSaga rebuilds a saga from storage.
func RestoreSaga(s SagaSnapshot) *Saga { return &Saga{s: s} }

// Snapshot returns the persistable state.
func (s *Saga) Snapshot() SagaSnapshot {
	c := s.s
	c.Pending = append([]string(nil), s.s.Pending...)
	return c
}

func (s *Saga) ID() string        { return s.s.ID }
func (s *Saga) State() SagaState  { return s.s.State }
func (s *Saga) PaymentID() string { return s.s.PaymentID }

func (s *Saga) touch(now time.Time) {
	s.s.UpdatedAt = now
	s.s.Version++
}

func (s *Saga) enter(state SagaState, now time.Time, t Timeouts) {
	s.s.State, s.s.StepDeadline = state, now.Add(t.Step)
	s.touch(now)
}

// OnReply handles a participant's reply. A reply that does not fit the current state (late, duplicated, or from a step
// that was already given up on) is Stale and changes nothing: it is safe to receive every reply any number of times.
func (s *Saga) OnReply(m saga.Message, now time.Time, t Timeouts) Result {
	switch s.s.State {
	case StateReservingStock:
		switch r := m.(type) {
		case saga.StockReserved:
			return s.stockReserved(now, t)
		case saga.StockReservationFailed:
			return s.compensate(r.Reason, nil, now, t) // the participant already rolled back
		}
	case StateApplyingDiscount:
		switch r := m.(type) {
		case saga.DiscountsApplied:
			total := r.TotalDiscount
			s.s.DiscountTotal = total
			res := s.startPayment(now, t)
			res.TotalDiscount = &total
			res.Orders = append([]OrderStep{{To: StatusDiscountApplied}}, res.Orders...)
			return res
		case saga.DiscountsRejected:
			return s.compensate(r.Reason, []string{saga.CmdReleaseStock}, now, t) // nothing was recorded by discount-service
		}
	case StateProcessingPay:
		switch r := m.(type) {
		case saga.PaymentCompleted:
			s.s.PaymentID = r.PaymentID
			s.enter(StateCommittingStock, now, t)
			return Result{Commands: []string{saga.CmdCommitStock}}
		case saga.PaymentFailed:
			return s.compensate(r.Reason, s.undoDiscountAndStock(), now, t)
		}
	case StateCommittingStock:
		if _, ok := m.(saga.StockCommitted); ok {
			s.enter(StateCompleted, now, t)
			return Result{Orders: []OrderStep{{To: StatusConfirmed}}, Finished: StateCompleted}
		}
	case StateCompensating:
		return s.acknowledge(m, now, t)
	}
	return Result{Stale: true}
}

func (s *Saga) stockReserved(now time.Time, t Timeouts) Result {
	if s.s.HasDiscounts {
		s.enter(StateApplyingDiscount, now, t)
		return Result{Commands: []string{saga.CmdApplyDiscounts}, Orders: []OrderStep{{To: StatusStockReserved}}}
	}
	res := s.startPayment(now, t)
	res.Orders = append([]OrderStep{{To: StatusStockReserved}}, res.Orders...)
	return res
}

func (s *Saga) startPayment(now time.Time, t Timeouts) Result {
	s.enter(StateProcessingPay, now, t)
	return Result{Commands: []string{saga.CmdProcessPayment}, Orders: []OrderStep{{To: StatusPaymentProcessing}}}
}

// undoDiscountAndStock is what a failure after the discount step must undo, most recent step first.
func (s *Saga) undoDiscountAndStock() []string {
	if s.s.HasDiscounts {
		return []string{saga.CmdReleaseDiscount, saga.CmdReleaseStock}
	}
	return []string{saga.CmdReleaseStock}
}

// compensate enters COMPENSATING and sends every undo command at once (they are independent and idempotent). With
// nothing to undo the saga fails on the spot.
func (s *Saga) compensate(reason string, undo []string, now time.Time, t Timeouts) Result {
	s.s.Reason = reason
	steps := []OrderStep{{To: StatusCancelling, Reason: reason}}
	if len(undo) == 0 {
		s.enter(StateFailed, now, t)
		return Result{Orders: append(steps, OrderStep{To: StatusCancelled}), Finished: StateFailed}
	}
	s.s.Pending = append([]string(nil), undo...)
	s.enter(StateCompensating, now, t)
	s.s.StepDeadline = now.Add(t.Compensation)
	return Result{Commands: undo, Orders: steps, Compensating: true}
}

var ackOf = map[string]string{
	saga.ReplyStockReleased:     saga.CmdReleaseStock,
	saga.ReplyDiscountsReleased: saga.CmdReleaseDiscount,
	saga.ReplyPaymentRefunded:   saga.CmdRefundPayment,
}

// acknowledge records a compensation reply; when none is pending any more the saga is FAILED.
func (s *Saga) acknowledge(m saga.Message, now time.Time, t Timeouts) Result {
	cmd, ok := ackOf[m.MessageType()]
	if !ok {
		return Result{Stale: true} // a late forward reply: the compensation will undo whatever it did
	}
	idx := -1
	for i, p := range s.s.Pending {
		if p == cmd {
			idx = i
		}
	}
	if idx < 0 {
		return Result{Stale: true} // already acknowledged
	}
	s.s.Pending = append(s.s.Pending[:idx:idx], s.s.Pending[idx+1:]...)
	if len(s.s.Pending) > 0 {
		s.touch(now)
		return Result{}
	}
	s.enter(StateFailed, now, t)
	return Result{Orders: []OrderStep{{To: StatusCancelled}}, Finished: StateFailed}
}

// OnTimeout is called when the step deadline has passed.
//   - a forward step that did not answer is given up on: its compensations are sent (the step may or may not have run)
//   - after the payment succeeded there is no way back: CommitStock is sent again
//   - compensations are re-sent until acknowledged
func (s *Saga) OnTimeout(now time.Time, t Timeouts) Result {
	if s.s.State.Terminal() {
		return Result{Stale: true}
	}
	timedOut := s.s.State
	s.s.Attempts++
	var res Result
	switch timedOut {
	case StateReservingStock:
		res = s.compensate(saga.ReasonStepTimeout, []string{saga.CmdReleaseStock}, now, t)
	case StateApplyingDiscount:
		res = s.compensate(saga.ReasonStepTimeout, []string{saga.CmdReleaseDiscount, saga.CmdReleaseStock}, now, t)
	case StateProcessingPay:
		res = s.compensate(saga.ReasonStepTimeout, append([]string{saga.CmdRefundPayment}, s.undoDiscountAndStock()...), now, t)
	case StateCommittingStock: // the pivot: payment is done, only forward
		s.s.StepDeadline = now.Add(t.Step)
		s.touch(now)
		return Result{Commands: []string{saga.CmdCommitStock}, TimedOut: timedOut, Resent: true}
	case StateCompensating:
		s.s.StepDeadline = now.Add(t.Compensation)
		s.touch(now)
		return Result{Commands: append([]string(nil), s.s.Pending...), TimedOut: timedOut, Resent: true}
	}
	res.TimedOut = timedOut
	return res
}

// Package usecase holds the application logic of order-service: the review and placing of orders, and the saga
// orchestration (reply handling and the timeout sweeper). It depends on the domain and on the small interfaces below.
package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// TxManager runs fn in one database transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Outbox adds messages in the transaction of ctx.
type Outbox interface {
	Add(ctx context.Context, msgs ...outbox.Message) error
}

// SagaMetrics is what the orchestration reports (docs/14: saga_started_total, saga_completed_total,
// saga_failed_total, saga_compensations_total, saga_step_timeouts_total, saga_stale_replies_total, saga_duration_seconds).
type SagaMetrics interface {
	Started()
	Finished(state domain.SagaState, reason string, took time.Duration)
	Compensation(command string)
	StepTimeout(step domain.SagaState)
	StaleReply()
}

type nopMetrics struct{}

func (nopMetrics) Started()                                         {}
func (nopMetrics) Finished(domain.SagaState, string, time.Duration) {}
func (nopMetrics) Compensation(string)                              {}
func (nopMetrics) StepTimeout(domain.SagaState)                     {}
func (nopMetrics) StaleReply()                                      {}

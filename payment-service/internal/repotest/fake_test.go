package repotest_test

import (
	"testing"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/payment-service/internal/fake"
	"github.com/an-vt/ecom/payment-service/internal/repotest"
)

func TestFakeSatisfiesTheContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) domain.PaymentRepository { return fake.NewWorld().Payments() })
}

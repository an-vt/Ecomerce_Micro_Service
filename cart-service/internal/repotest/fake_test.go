package repotest_test

import (
	"testing"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/cart-service/internal/fake"
	"github.com/an-vt/ecom/cart-service/internal/repotest"
)

func TestFakeSatisfiesTheContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) domain.CartRepository { return fake.NewCarts() })
}

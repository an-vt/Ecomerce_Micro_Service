package repotest_test

import (
	"testing"

	"github.com/an-vt/ecom/discount-service/internal/fake"
	"github.com/an-vt/ecom/discount-service/internal/repotest"
)

func TestFakesSatisfyTheContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		w := fake.NewWorld()
		return repotest.Repos{Discounts: w.Discounts(), Usages: w.Usages()}
	})
}

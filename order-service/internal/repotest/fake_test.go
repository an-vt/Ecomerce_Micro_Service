package repotest_test

import (
	"testing"

	"github.com/an-vt/ecom/order-service/internal/fake"
	"github.com/an-vt/ecom/order-service/internal/repotest"
)

func TestFakesSatisfyTheContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		w := fake.NewWorld()
		return repotest.Repos{Orders: w.Orders(), Sagas: w.Sagas(), Keys: w.Keys()}
	})
}

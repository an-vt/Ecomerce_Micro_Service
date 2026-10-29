package repotest_test

import (
	"testing"

	"github.com/an-vt/ecom/inventory-service/internal/fake"
	"github.com/an-vt/ecom/inventory-service/internal/repotest"
)

func TestFakesSatisfyTheContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		w := fake.NewWorld()
		return repotest.Repos{Inventories: w.Inventories(), Reservations: w.Reservations()}
	})
}

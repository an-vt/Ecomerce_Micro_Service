package repotest_test

import (
	"testing"

	"github.com/an-vt/ecom/catalog-service/internal/fake"
	"github.com/an-vt/ecom/catalog-service/internal/repotest"
)

// The in-memory fakes must satisfy the same contract as the MongoDB repositories.
func TestFakesSatisfyTheContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		w := fake.NewWorld()
		return repotest.Repos{Products: w.Products(), Spus: w.Spus(), Comments: w.Comments(), Shops: w.Shops()}
	})
}

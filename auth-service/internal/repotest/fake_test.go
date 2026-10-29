package repotest_test

import (
	"testing"

	"github.com/an-vt/ecom/auth-service/internal/fake"
	"github.com/an-vt/ecom/auth-service/internal/repotest"
)

// The in-memory fakes must satisfy the same contract as the PostgreSQL repositories.
func TestFakesSatisfyTheContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		w := fake.NewWorld()
		return repotest.Repos{Accounts: w.Accounts(), Sessions: w.Sessions(), Otps: w.Otps(), ApiKeys: w.ApiKeys(), Rbac: w.Rbac()}
	})
}

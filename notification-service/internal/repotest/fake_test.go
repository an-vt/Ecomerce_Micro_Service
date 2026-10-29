package repotest_test

import (
	"testing"

	"github.com/an-vt/ecom/notification-service/internal/fake"
	"github.com/an-vt/ecom/notification-service/internal/repotest"
)

func TestFakesSatisfyTheContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		w := fake.NewWorld()
		return repotest.Repos{Notifications: w.Notifications(), Templates: w.Templates(), Emails: w.Emails()}
	})
}

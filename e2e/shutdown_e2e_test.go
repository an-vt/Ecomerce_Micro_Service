//go:build e2e

package e2e_test

import (
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
)

// A service that receives SIGTERM (docker stop, a deploy, an autoscaler) while it is consuming must finish what it holds,
// exit cleanly and lose nothing; the work that arrived meanwhile waits in Kafka and is done when it is back.
func TestInventoryStoppedMidCheckoutLosesAndDuplicatesNothing(t *testing.T) {
	c := e2ekit.NewClient(t)
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available")
	}
	const orders = 8
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, orders+2, 1000)
	buyers := make([]string, orders)
	for i := range buyers {
		buyers[i] = signUpBuyer(t, c)
	}

	t.Cleanup(func() { _ = exec.Command("docker", "start", "ecom-inventory").Run() })
	ids := make([]string, orders)
	var wg sync.WaitGroup
	for i := 0; i < orders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := placeOrder(t, c, buyers[i], fmt.Sprintf("e2e-shutdown-%d-%d", time.Now().UnixNano(), i), orderBody(shop.id, pid, 1, "", "tok_ok"))
			if assert.Equal(t, 202, r.Status, string(r.Body)) {
				ids[i] = r.String("metadata.orderId")
			}
		}(i)
	}
	wg.Wait()

	// SIGTERM while the reservations are being consumed
	inFlight := 0
	for i, id := range ids {
		if c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/orders/" + id, Token: buyers[i]}).String("metadata.order_status") != "CONFIRMED" {
			inFlight++
		}
	}
	t.Logf("%d of %d orders were still in flight when SIGTERM arrived", inFlight, orders)
	require.Positive(t, inFlight, "the test is only meaningful if the stop lands while checkouts are running")
	docker(t, "stop", "-t", "20", "ecom-inventory")
	code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", "ecom-inventory")
	assert.Equal(t, "0", code, "a clean shutdown exits 0; 137 would mean it had to be killed")

	docker(t, "start", "ecom-inventory")
	for i, id := range ids {
		require.NotEmpty(t, id, "order %d", i)
		waitOrder(t, c, buyers[i], id, "CONFIRMED")
	}
	// exactly one unit per order: nothing reserved twice, nothing lost, nothing left on hold
	waitForStock(t, c, pid, 2, 0)
}

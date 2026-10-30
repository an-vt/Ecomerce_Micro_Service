//go:build e2e

package e2e_test

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	require.NoError(t, err, "docker %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// Scenario 9: a dead dependency must make the gateway answer 503 fast, not slowly; and it must recover on its own.
func TestCircuitBreakerOpensWhenAServiceIsDownAndClosesAgain(t *testing.T) {
	c := e2ekit.NewClient(t)
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available")
	}
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 3, 1000)
	get := func() *e2ekit.Response { return c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/inventory/" + pid}) }
	require.Equal(t, 200, get().Status, "healthy to begin with")

	docker(t, "stop", "ecom-inventory")
	t.Cleanup(func() { _ = exec.Command("docker", "start", "ecom-inventory").Run() })

	// every call fails while the service is down; after the threshold the breaker opens. The call that trips it may still
	// have retried with backoff, so speed is judged on the calls that come after.
	var open *e2ekit.Response
	require.Eventually(t, func() bool {
		r := get()
		if r.Status == 503 && r.Header.Get("Retry-After") != "" { // Retry-After is only set by an OPEN circuit
			open = r
			return true
		}
		return false
	}, 60*time.Second, 50*time.Millisecond, "the circuit never opened")
	assert.Equal(t, "UPSTREAM_UNAVAILABLE", open.Code())
	retryAfter, err := strconv.Atoi(open.Header.Get("Retry-After"))
	require.NoError(t, err)
	assert.Positive(t, retryAfter)

	var fastest, slowest time.Duration
	for i := 0; i < 10; i++ {
		start := time.Now()
		r := get()
		took := time.Since(start)
		require.Equal(t, 503, r.Status)
		assert.NotEmpty(t, r.Header.Get("Retry-After"))
		if i == 0 || took < fastest {
			fastest = took
		}
		slowest = max(slowest, took)
	}
	assert.Less(t, slowest, 50*time.Millisecond, "an open circuit answers without calling the dead service (fastest %s)", fastest)

	// other services are unaffected: the breaker is per upstream
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product"}).Status, "catalog still answers")

	// bring it back: after the reset timeout a trial call succeeds and the circuit closes
	docker(t, "start", "ecom-inventory")
	require.Eventually(t, func() bool { return get().Status == 200 }, 90*time.Second, time.Second, "the gateway never recovered")
	for i := 0; i < 5; i++ {
		assert.Equal(t, 200, get().Status, "and stays closed")
	}
}

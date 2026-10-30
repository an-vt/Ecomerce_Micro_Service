//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
)

func getJSON(url string, into any) error {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// Scenario 10, first half: every service exposes /metrics, and Prometheus can scrape all of them.
func TestEveryServiceExposesMetricsAndPrometheusScrapesThem(t *testing.T) {
	_ = e2ekit.NewClient(t) // the stack must be up
	ports := map[string]int{"api-gateway": 8088, "auth-service": 8101, "catalog-service": 8102, "inventory-service": 8103, "cart-service": 8104,
		"discount-service": 8105, "order-service": 8106, "payment-service": 8107, "notification-service": 8108}
	for name, port := range ports {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
		if assert.NoError(t, err, name) {
			assert.Equal(t, 200, resp.StatusCode, name)
			_ = resp.Body.Close()
		}
	}
	prom := e2ekit.Env("PROMETHEUS_URL", "http://127.0.0.1:9090")
	e2ekit.RequireReachable(t, "Prometheus", "127.0.0.1:9090")
	var res struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, getJSON(prom+`/api/v1/query?query=up%7Bjob%3D%22ecom-services%22%7D`, &res))
	up := map[string]string{}
	for _, r := range res.Data.Result {
		up[r.Metric["service"]] = fmt.Sprint(r.Value[1])
	}
	for name := range ports {
		assert.Equal(t, "1", up[name], "%s is scraped and up", name)
	}
}

// Scenario 10, second half: one checkout is one trace across the services, found in Tempo by the id the gateway returned.
func TestACheckoutIsOneTraceAcrossTheServices(t *testing.T) {
	c := e2ekit.NewClient(t)
	e2ekit.RequireReachable(t, "Tempo", "127.0.0.1:3200")
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 5, 1000)
	buyer := signUpBuyer(t, c)

	placed := placeOrder(t, c, buyer, fmt.Sprintf("e2e-trace-%d", time.Now().UnixNano()), orderBody(shop.id, pid, 1, "", "tok_ok"))
	require.Equal(t, 202, placed.Status, string(placed.Body))
	traceID := placed.Header.Get("X-Trace-Id")
	require.Len(t, traceID, 32, "the gateway returns the id of the trace it started")
	waitOrder(t, c, buyer, placed.String("metadata.orderId"), "CONFIRMED")

	tempo := e2ekit.Env("TEMPO_URL", "http://127.0.0.1:3200")
	want := []string{"api-gateway", "order-service", "inventory-service", "payment-service", "notification-service"}
	var services map[string]bool
	require.Eventually(t, func() bool {
		var tr struct {
			Batches []struct {
				Resource struct {
					Attributes []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue string `json:"stringValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"resource"`
			} `json:"batches"`
		}
		if getJSON(tempo+"/api/traces/"+traceID, &tr) != nil {
			return false
		}
		services = map[string]bool{}
		for _, b := range tr.Batches {
			for _, a := range b.Resource.Attributes {
				if a.Key == "service.name" {
					services[a.Value.StringValue] = true
				}
			}
		}
		for _, w := range want {
			if !services[w] {
				return false
			}
		}
		return true
	}, 90*time.Second, time.Second, "Tempo should hold one trace with spans of %v, has %v", want, services)
}

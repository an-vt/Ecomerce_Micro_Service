//go:build e2e

package e2e_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
	"github.com/an-vt/ecom/platform-go/saga"
)

func newProduct(t *testing.T, c *e2ekit.Client, shop shopSession, qty int) string {
	t.Helper()
	r := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product", Token: shop.token, Body: map[string]any{
		"product_type": "Furniture", "product_name": fmt.Sprintf("E2E Chair %d", time.Now().UnixNano()), "product_price": 500000, "product_quantity": qty,
		"product_attributes": map[string]any{"brand": "b", "size": "L", "material": "oak"}}})
	require.Equal(t, 201, r.Status, string(r.Body))
	return r.String("metadata._id")
}

func waitForStock(t *testing.T, c *e2ekit.Client, id string, stock, reserved float64) {
	t.Helper()
	require.Eventually(t, func() bool {
		r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/inventory/" + id})
		return r.Status == 200 && r.Get("metadata.inven_stock") == stock && r.Get("metadata.inven_reserved") == reserved
	}, 20*time.Second, 200*time.Millisecond, "stock of %s should become %v/%v", id, stock, reserved)
}

func TestStockFollowsTheCatalogOverKafka(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	id := newProduct(t, c, shop, 12)

	// catalog.product.created -> inventory creates the record, with no direct call between the services
	waitForStock(t, c, id, 12, 0)

	// the owner adds stock; another shop cannot
	add := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/inventory", Token: shop.token, Body: map[string]any{"productId": id, "stock": 8, "location": "Hall 4"}})
	require.Equal(t, 200, add.Status, string(add.Body))
	assert.EqualValues(t, 20, add.Get("metadata.inven_stock"))
	assert.Equal(t, "Hall 4", add.String("metadata.inven_location"))
	other := signUpShop(t, c)
	forbidden := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/inventory", Token: other.token, Body: map[string]any{"productId": id, "stock": 1}})
	assert.Equal(t, 403, forbidden.Status)
	assert.Equal(t, "NOT_OWNER", forbidden.Code())
	assert.Equal(t, 404, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/inventory", Token: shop.token, Body: map[string]any{"productId": "ghost", "stock": 1}}).Status)
	assert.Equal(t, 404, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/inventory/ghost"}).Status)
	assert.Equal(t, 400, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/inventory", Token: shop.token, Body: map[string]any{"productId": id, "stock": -3}}).Status)

	// a quantity change in the catalog syncs through catalog.product.updated
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "PATCH", Path: "/v1/api/product/" + id, Token: shop.token, Body: map[string]any{"product_quantity": 50}}).Status)
	waitForStock(t, c, id, 50, 0)
}

func TestSagaParticipantReservesCommitsAndReleases(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	id := newProduct(t, c, shop, 10)
	waitForStock(t, c, id, 10, 0)

	// reserve 4 for order A
	orderA := fmt.Sprintf("e2e-order-a-%d", time.Now().UnixNano())
	e2ekit.SendSagaCommand(t, saga.TopicInventoryCommands, saga.ReserveStock{OrderID: orderA, Items: []saga.Item{{ProductID: id, Quantity: 4}}})
	e2ekit.WaitForReply(t, orderA, saga.ReplyStockReserved, nil)
	waitForStock(t, c, id, 6, 4)

	// order B wants 7 but only 6 are left: refused, and nothing is held
	orderB := fmt.Sprintf("e2e-order-b-%d", time.Now().UnixNano())
	e2ekit.SendSagaCommand(t, saga.TopicInventoryCommands, saga.ReserveStock{OrderID: orderB, Items: []saga.Item{{ProductID: id, Quantity: 7}}})
	var failed saga.StockReservationFailed
	e2ekit.WaitForReply(t, orderB, saga.ReplyStockReservationFailed, &failed)
	assert.Equal(t, saga.ReasonInsufficientStock, failed.Reason)
	assert.Equal(t, id, failed.ProductID)
	waitForStock(t, c, id, 6, 4)

	// order A is paid: commit sells the held units
	e2ekit.SendSagaCommand(t, saga.TopicInventoryCommands, saga.CommitStock{OrderID: orderA})
	e2ekit.WaitForReply(t, orderA, saga.ReplyStockCommitted, nil)
	waitForStock(t, c, id, 6, 0)

	// order C is reserved then compensated: the units come back
	orderC := fmt.Sprintf("e2e-order-c-%d", time.Now().UnixNano())
	e2ekit.SendSagaCommand(t, saga.TopicInventoryCommands, saga.ReserveStock{OrderID: orderC, Items: []saga.Item{{ProductID: id, Quantity: 5}}})
	e2ekit.WaitForReply(t, orderC, saga.ReplyStockReserved, nil)
	waitForStock(t, c, id, 1, 5)
	e2ekit.SendSagaCommand(t, saga.TopicInventoryCommands, saga.ReleaseStock{OrderID: orderC})
	e2ekit.WaitForReply(t, orderC, saga.ReplyStockReleased, nil)
	waitForStock(t, c, id, 6, 0)
	// releasing again, or releasing an order that never reserved anything, is answered and changes nothing
	e2ekit.SendSagaCommand(t, saga.TopicInventoryCommands, saga.ReleaseStock{OrderID: orderC})
	e2ekit.SendSagaCommand(t, saga.TopicInventoryCommands, saga.ReleaseStock{OrderID: "never-reserved-" + orderC})
	e2ekit.WaitForReply(t, "never-reserved-"+orderC, saga.ReplyStockReleased, nil)
	waitForStock(t, c, id, 6, 0)
}

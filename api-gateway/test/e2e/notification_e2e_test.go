//go:build e2e

package e2e_test

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
)

var hrefPattern = regexp.MustCompile(`href="([^"]+)"`)

// The monolith's RabbitMQ email worker is gone: auth publishes the event, notification-service renders the template and
// sends the mail, Mailpit catches it. The link in the mail works.
func TestVerificationEmailReachesMailpitAndItsLinkWorks(t *testing.T) {
	c := e2ekit.NewClient(t)
	email := e2ekit.UniqueEmail("mail")
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/user/new_user", Body: map[string]string{"email": email}}).Status)

	mail := e2ekit.WaitForMail(t, email)
	assert.Equal(t, "Confirm your email", mail.Subject)
	assert.Equal(t, []string{email}, mail.To)
	assert.NotContains(t, mail.HTML, "{{", "every placeholder was replaced")

	m := hrefPattern.FindStringSubmatch(mail.HTML)
	require.Len(t, m, 2, "the mail has a link: %s", mail.HTML)
	link, err := url.Parse(html.UnescapeString(m[1])) // &amp; in the attribute is &
	require.NoError(t, err)
	token := link.Query().Get("token")
	require.NotEmpty(t, token)

	verified := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/user/welcome_back?token=" + url.QueryEscape(token)})
	require.Equal(t, 200, verified.Status, string(verified.Body))
	assert.NotEmpty(t, verified.String("metadata.tokens.accessToken"), "the buyer is signed in by the link from the email")
}

func notificationsOf(t *testing.T, c *e2ekit.Client, token, typ string) []map[string]any {
	t.Helper()
	r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/notification?limit=100&type=" + typ, Token: token})
	require.Equal(t, 200, r.Status, string(r.Body))
	var out []map[string]any
	for _, n := range r.Get("metadata.notifications").([]any) {
		out = append(out, n.(map[string]any))
	}
	return out
}

func waitNotification(t *testing.T, c *e2ekit.Client, token, typ string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	var found map[string]any
	require.Eventually(t, func() bool {
		for _, n := range notificationsOf(t, c, token, typ) {
			if match(n) {
				found = n
				return true
			}
		}
		return false
	}, 30*time.Second, 300*time.Millisecond, "no %s notification matched", typ)
	return found
}

func optionOf(n map[string]any, key string) any { return n["noti_options"].(map[string]any)[key] }

func TestNotificationsFollowProductsDiscountsAndOrders(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 5, 100000)
	code := uniqueCode("NTF")
	require.Equal(t, 201, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: shop.token, Body: discountBody(code, nil)}).Status)
	alice, bob := signUpBuyer(t, c), signUpBuyer(t, c)

	// everybody hears about a new product and a new voucher (broadcasts)
	product := waitNotification(t, c, alice, "SHOP-001", func(n map[string]any) bool { return optionOf(n, "productId") == pid })
	assert.Equal(t, "*", product["noti_receiverId"])
	assert.Equal(t, shop.id, product["noti_senderId"])
	assert.Contains(t, product["noti_content"], "A shop just added a new product: E2E Cart Shirt")
	voucher := waitNotification(t, c, bob, "PROMOTION-001", func(n map[string]any) bool { return optionOf(n, "code") == code })
	assert.Contains(t, voucher["noti_content"], code)
	assert.NotContains(t, voucher["noti_content"], "{{")

	// alice's orders notify alice only: one confirmed, one declined
	ok := placeOrder(t, c, alice, fmt.Sprintf("e2e-%d", time.Now().UnixNano()), orderBody(shop.id, pid, 1, "", "tok_ok"))
	require.Equal(t, 202, ok.Status, string(ok.Body))
	okID := ok.String("metadata.orderId")
	waitOrder(t, c, alice, okID, "CONFIRMED")
	confirmed := waitNotification(t, c, alice, "ORDER-001", func(n map[string]any) bool { return optionOf(n, "orderId") == okID })
	assert.Contains(t, confirmed["noti_content"], okID)
	assert.Contains(t, confirmed["noti_content"], "was confirmed. Total: 100000")

	declined := placeOrder(t, c, alice, fmt.Sprintf("e2e-%d", time.Now().UnixNano()), orderBody(shop.id, pid, 1, "", "tok_fail"))
	require.Equal(t, 202, declined.Status)
	failID := declined.String("metadata.orderId")
	waitOrder(t, c, alice, failID, "CANCELLED")
	cancelled := waitNotification(t, c, alice, "ORDER-002", func(n map[string]any) bool { return optionOf(n, "orderId") == failID })
	assert.Contains(t, cancelled["noti_content"], "your card was declined", "the buyer is told why, in words")
	assert.Equal(t, "CARD_DECLINED", optionOf(cancelled, "reason"))

	// newest first across types, and nothing of alice's reaches bob
	all := notificationsOf(t, c, alice, "ALL")
	idx := map[string]int{}
	for i, n := range all {
		idx[fmt.Sprint(n["_id"])] = i
	}
	assert.Less(t, idx[fmt.Sprint(cancelled["_id"])], idx[fmt.Sprint(confirmed["_id"])], "the declined order is newer than the confirmed one")
	assert.Less(t, idx[fmt.Sprint(confirmed["_id"])], idx[fmt.Sprint(product["_id"])])
	for _, n := range notificationsOf(t, c, bob, "ALL") {
		typ := n["noti_type"]
		assert.False(t, typ == "ORDER-001" || typ == "ORDER-002", "bob must not see alice's order notifications: %v", n)
	}
	assert.Equal(t, 400, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/notification?type=BOGUS", Token: alice}).Status)
	assert.Equal(t, 401, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/notification"}).Status)
}

func TestAdministratorsManageTemplates(t *testing.T) {
	c := e2ekit.NewClient(t)
	login := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/user/login", Body: map[string]string{
		"email": e2ekit.Env("SEED_ADMIN_EMAIL", "admin@ecom.local"), "password": e2ekit.Env("SEED_ADMIN_PASSWORD", "admin-change-me-123")}})
	require.Equal(t, 200, login.Status, "run `make seed` first: %s", string(login.Body))
	admin := login.String("metadata.tokens.accessToken")
	name := fmt.Sprintf("E2E template %d", time.Now().UnixNano())
	body := map[string]any{"name": name, "html": "<p>Order {{orderId}} for {{city}}</p>"}

	created := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/email/new_template", Token: admin, Body: body})
	require.Equal(t, 201, created.Status, string(created.Body))
	assert.Equal(t, name, created.String("metadata.tem_name"))
	dup := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/email/new_template", Token: admin, Body: body})
	assert.Equal(t, 409, dup.Status)
	assert.Equal(t, "TEMPLATE_EXISTS", dup.Code())

	buyer := signUpBuyer(t, c)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/email/new_template", Token: buyer, Body: body}).Status, "buyers may not")
	assert.Equal(t, 400, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/email/new_template", Token: admin, Body: map[string]any{"name": "x", "html": "<p>x</p>"}}).Status)
}

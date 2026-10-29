package http_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
)

func TestNotificationsNeedALogin(t *testing.T) {
	g := newGateway(t)
	assert.Equal(t, 401, g.call("GET", "/v1/api/notification", "").Code)
	for _, token := range []string{g.token("u1", "USER", "user"), g.token("s1", "SHOP", "shop")} {
		assert.Equal(t, 200, g.as(token, "GET", "/v1/api/notification", "").Code, "buyers and shops both have notifications")
	}
}

func TestListNotificationsKeepsTheMonolithNamesAndNeverTakesAReceiver(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	r := g.as(user, "GET", "/v1/api/notification?type=ORDER-001&isRead=false&page=2&limit=5&userId=someone-else", "")
	require.Equal(t, 200, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.EqualValues(t, 2, meta["total"])
	assert.EqualValues(t, 2, meta["page"])
	first := meta["notifications"].([]any)[0].(map[string]any)
	assert.Equal(t, "ORDER-001", first["noti_type"])
	assert.Equal(t, "order-service", first["noti_senderId"])
	assert.Equal(t, "Your order o1 was confirmed", first["noti_content"])
	assert.EqualValues(t, 5000, first["noti_options"].(map[string]any)["total"])
	assert.Equal(t, "*", meta["notifications"].([]any)[1].(map[string]any)["noti_receiverId"])
	assert.Equal(t, map[string]any{}, meta["notifications"].([]any)[1].(map[string]any)["noti_options"], "options are an object, never null")

	g.notification.mu.Lock()
	defer g.notification.mu.Unlock()
	req := g.notification.listReq
	assert.Equal(t, "ORDER-001", req.GetType())
	require.NotNil(t, req.IsRead)
	assert.False(t, req.GetIsRead())
	assert.EqualValues(t, 5, req.GetLimit())
	assert.Equal(t, "u1", g.notification.last.Get("x-account-id")[0], "the receiver is the token's account; the userId in the query string is ignored")
}

func TestListNotificationsValidatesIsRead(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	assert.Equal(t, 400, g.as(user, "GET", "/v1/api/notification?isRead=maybe", "").Code)
	g.notification.mu.Lock()
	g.notification.listReq = nil
	g.notification.mu.Unlock()
	require.Equal(t, 200, g.as(user, "GET", "/v1/api/notification", "").Code)
	g.notification.mu.Lock()
	assert.Nil(t, g.notification.listReq.IsRead, "no filter when the parameter is absent")
	g.notification.mu.Unlock()

	g.notification.fail = errs.InvalidArgument("VALIDATION_FAILED", "unknown notification type")
	assert.Equal(t, 400, g.as(user, "GET", "/v1/api/notification?type=BOGUS", "").Code)
}

func TestOnlyAdministratorsCreateTemplates(t *testing.T) {
	g := newGateway(t)
	body := `{"name":"Order shipped","html":"<p>{{orderId}}</p>"}`
	assert.Equal(t, 401, g.call("POST", "/v1/api/email/new_template", body).Code)
	assert.Equal(t, 403, g.as(g.token("u1", "USER", "user"), "POST", "/v1/api/email/new_template", body).Code, "a buyer has no createAny:template grant")
	assert.Equal(t, 403, g.as(g.token("s1", "SHOP", "shop"), "POST", "/v1/api/email/new_template", body).Code)

	r := g.as(g.token("a1", "USER", "admin"), "POST", "/v1/api/email/new_template", body)
	require.Equal(t, 201, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.Equal(t, "Order shipped", meta["tem_name"])
	assert.Equal(t, "active", meta["tem_status"])
	g.notification.mu.Lock()
	assert.Equal(t, "<p>{{orderId}}</p>", g.notification.createReq.GetHtml())
	g.notification.mu.Unlock()
}

func TestTemplateValidationAndErrors(t *testing.T) {
	g := newGateway(t)
	admin := g.token("a1", "USER", "admin")
	assert.Equal(t, 400, g.as(admin, "POST", "/v1/api/email/new_template", `{}`).Code)
	assert.Equal(t, 400, g.as(admin, "POST", "/v1/api/email/new_template", `{"name":"x"}`).Code)
	assert.Equal(t, 400, g.as(admin, "POST", "/v1/api/email/new_template", `{"name":"ok name","html":"<p>x</p>","tem_id":"1"}`).Code, "unknown fields are rejected")
	g.notification.fail = errs.AlreadyExists("TEMPLATE_EXISTS", "a template with this name already exists")
	r := g.as(admin, "POST", "/v1/api/email/new_template", `{"name":"Order shipped","html":"<p>x</p>"}`)
	assert.Equal(t, 409, r.Code)
	assert.Equal(t, "TEMPLATE_EXISTS", r.errCode())
}

package http

import (
	"encoding/json"
	stdhttp "net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/an-vt/ecom/platform-go/errs"
	notificationv1 "github.com/an-vt/ecom/proto/gen/go/notification/v1"
)

// Notification holds the notification and email-template routes. The receiver of a listing is always the token's
// account, never a query parameter (the monolith read the user id from the query string).
type Notification struct {
	client notificationv1.NotificationServiceClient
}

type notificationJSON struct {
	ID         string         `json:"_id"`
	Type       string         `json:"noti_type"`
	SenderID   string         `json:"noti_senderId"`
	ReceiverID string         `json:"noti_receiverId"`
	Content    string         `json:"noti_content"`
	Options    map[string]any `json:"noti_options"`
	IsRead     bool           `json:"noti_isRead"`
	CreatedAt  string         `json:"createdAt"`
}

func notificationOf(n *notificationv1.Notification) notificationJSON {
	opts := map[string]any{}
	if raw := n.GetOptionsJson(); raw != "" {
		_ = json.Unmarshal([]byte(raw), &opts)
	}
	return notificationJSON{ID: n.GetId(), Type: n.GetType(), SenderID: n.GetSenderId(), ReceiverID: n.GetReceiverId(), Content: n.GetContent(),
		Options: opts, IsRead: n.GetIsRead(), CreatedAt: n.GetCreatedAt()}
}

func (h *Notification) list(c *gin.Context) {
	req := &notificationv1.ListNotificationsRequest{Type: c.Query("type"), Page: int32(intQuery(c, "page")), Limit: int32(intQuery(c, "limit"))}
	if raw := c.Query("isRead"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			fail(c, errs.Validation("isRead must be true or false"))
			return
		}
		req.IsRead = &v
	}
	r, err := h.client.ListNotifications(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]notificationJSON, 0, len(r.GetNotifications()))
	for _, n := range r.GetNotifications() {
		items = append(items, notificationOf(n))
	}
	ok(c, stdhttp.StatusOK, "Notifications", gin.H{"notifications": items, "total": r.GetTotal(), "page": max(intQuery(c, "page"), 1), "limit": intQuery(c, "limit")})
}

func (h *Notification) createTemplate(c *gin.Context) {
	var in struct {
		Name string `json:"name" binding:"required"`
		HTML string `json:"html" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "name and html are required")
		return
	}
	r, err := h.client.CreateTemplate(c.Request.Context(), &notificationv1.CreateTemplateRequest{Name: in.Name, Html: in.HTML})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusCreated, "Template created", gin.H{"_id": r.GetId(), "tem_name": r.GetName(), "tem_html": r.GetHtml(), "tem_status": r.GetStatus(), "createdAt": r.GetCreatedAt()})
}

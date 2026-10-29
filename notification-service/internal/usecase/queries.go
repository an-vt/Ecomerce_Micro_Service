package usecase

import (
	"context"
	"strings"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/pagination"
)

// ListNotifications lists what the caller should see.
type ListNotifications struct{ d *Deps }

// ListInput are the filters of a listing.
type ListInput struct {
	Type   string // "" or ALL: every type
	IsRead *bool
	Page   int
	Limit  int
}

// Execute returns the caller's own notifications and the broadcasts, newest first. The receiver is the token's account,
// never a request field (the monolith took a user id from the query string). Broadcasts have no per-person read state,
// so they always show as unread, and isRead=true therefore lists personal notifications only.
func (u ListNotifications) Execute(ctx context.Context, in ListInput) (pagination.Result[*domain.Notification], error) {
	me, err := caller(ctx)
	if err != nil {
		return pagination.Result[*domain.Notification]{}, err
	}
	f := domain.ListFilter{ReceiverID: me.ID, IsRead: in.IsRead}
	if t := strings.TrimSpace(in.Type); t != "" && !strings.EqualFold(t, "ALL") {
		f.Type = domain.Type(strings.ToUpper(t))
		if !f.Type.Valid() {
			return pagination.Result[*domain.Notification]{}, errs.Validation("unknown notification type " + t)
		}
	}
	pg := pagination.New(in.Page, in.Limit)
	items, total, err := u.d.Notifications.List(ctx, f, pg.Offset(), pg.Limit)
	if err != nil {
		return pagination.Result[*domain.Notification]{}, err
	}
	return pagination.NewResult(pg, items, total), nil
}

// CreateTemplate adds an email template.
type CreateTemplate struct{ d *Deps }

// Execute validates and stores a template. Who may do this is decided at the gateway (RBAC createAny:template); the
// service only insists on an authenticated caller.
func (u CreateTemplate) Execute(ctx context.Context, name, html string) (*domain.Template, error) {
	if _, err := caller(ctx); err != nil {
		return nil, err
	}
	t, err := domain.NewTemplate(u.d.IDs.New(), name, html, u.d.Clock.Now())
	if err != nil {
		return nil, err
	}
	if err := u.d.Templates.Insert(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// EnsureTemplates makes sure the templates the service needs exist.
type EnsureTemplates struct{ d *Deps }

// Execute creates the default templates that are missing and leaves existing ones alone (an administrator may have
// edited them). It returns how many it created.
func (u EnsureTemplates) Execute(ctx context.Context) (int, error) {
	created := 0
	for _, dt := range domain.DefaultTemplates() {
		t, err := domain.NewTemplate(u.d.IDs.New(), dt.Name, dt.HTML, u.d.Clock.Now())
		if err != nil {
			return created, err
		}
		ok, err := u.d.Templates.InsertIfAbsent(ctx, t)
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	return created, nil
}

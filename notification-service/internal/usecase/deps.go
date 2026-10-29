// Package usecase holds the application logic of notification-service: turning Kafka events into emails and in-app
// notifications, and the two synchronous operations (list notifications, create a template). It depends on the domain
// and on its ports only.
package usecase

import (
	"context"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/principal"
)

// Deps is everything the use cases need.
type Deps struct {
	Notifications domain.NotificationRepository
	Templates     domain.TemplateRepository
	Emails        domain.EmailRepository
	Sender        domain.Sender
	Clock         clock.Clock
	IDs           idgen.Generator
}

func caller(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.From(ctx)
	if !ok {
		return principal.Principal{}, errs.Unauthenticated("UNAUTHENTICATED", "this call needs an authenticated caller")
	}
	return p, nil
}

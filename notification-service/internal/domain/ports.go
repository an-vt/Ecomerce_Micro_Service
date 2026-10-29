package domain

import "context"

// NotificationRepository stores notifications.
type NotificationRepository interface {
	// Insert stores a notification unless one already exists for its DedupeKey, and reports whether it did.
	Insert(ctx context.Context, n *Notification) (inserted bool, err error)
	// List returns a page of the account's notifications and the broadcasts, newest first, and how many match.
	List(ctx context.Context, f ListFilter, offset, limit int) ([]*Notification, int64, error)
}

// TemplateRepository stores email templates.
type TemplateRepository interface {
	// Insert stores a template; ErrTemplateExists when the name is taken.
	Insert(ctx context.Context, t *Template) error
	// InsertIfAbsent stores a template unless the name is taken (used for the defaults, which an administrator may
	// have edited), and reports whether it did.
	InsertIfAbsent(ctx context.Context, t *Template) (bool, error)
	// FindByName loads a template; ErrTemplateNotFound when absent.
	FindByName(ctx context.Context, name string) (*Template, error)
}

// EmailRepository stores the record of each email.
type EmailRepository interface {
	// Begin stores e unless an email with its DedupeKey exists, and returns the stored one either way.
	Begin(ctx context.Context, e *Email) (*Email, error)
	// Save writes the status, attempts and error of an email.
	Save(ctx context.Context, e *Email) error
}

// Sender delivers a message over SMTP.
type Sender interface {
	Send(ctx context.Context, m Mail) error
}

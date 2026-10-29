// Package domain holds the notification bounded context: in-app notifications, email templates and the renderer that
// fills them, and the record of each email sent. It knows nothing about MongoDB, SMTP or Kafka (enforced by depguard,
// docs/03 §2).
package domain

import "github.com/an-vt/ecom/platform-go/errs"

// Domain error codes (docs/05 §2.1).
const (
	CodeTemplateNotFound = "TEMPLATE_NOT_FOUND"
	CodeTemplateExists   = "TEMPLATE_EXISTS"
	CodeInvalidTemplate  = "INVALID_TEMPLATE"
	CodeMissingParam     = "MISSING_PARAM"
	CodeInvalidEmail     = "INVALID_EMAIL"
)

// ErrTemplateNotFound is returned when no template has the name.
func ErrTemplateNotFound(name string) error {
	return errs.NotFound(CodeTemplateNotFound, "no email template named "+name)
}

// ErrTemplateExists is returned when a template name is reused.
func ErrTemplateExists() error {
	return errs.AlreadyExists(CodeTemplateExists, "a template with this name already exists")
}

func invalidTemplate(msg string) error { return errs.InvalidArgument(CodeInvalidTemplate, msg) }

package domain

import (
	"strings"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

// EmailStatus of a sent email.
type EmailStatus string

const (
	EmailPending EmailStatus = "PENDING"
	EmailSent    EmailStatus = "SENT"
	EmailFailed  EmailStatus = "FAILED"
)

// Email is the record of one email: created before the SMTP call, so a redelivered message finds it and does not send
// the same email twice.
type Email struct {
	ID        string
	DedupeKey string // message id + template
	To        string
	Subject   string
	Template  string
	Status    EmailStatus
	Attempts  int
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewEmail starts the record of an email. The address must be a single plain address: CR, LF and list separators in a
// header value are how mail gets injected.
func NewEmail(id, dedupeKey, to, subject, template string, now time.Time) (*Email, error) {
	to = strings.TrimSpace(to)
	if id == "" || dedupeKey == "" || template == "" {
		return nil, errs.Validation("an email needs an id, the key of the message that caused it and a template")
	}
	if !validAddress(to) {
		return nil, errs.InvalidArgument(CodeInvalidEmail, "the recipient is not a plain email address")
	}
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errs.InvalidArgument(CodeInvalidEmail, "the subject must be one line")
	}
	return &Email{ID: id, DedupeKey: dedupeKey, To: to, Subject: subject, Template: template, Status: EmailPending, CreatedAt: now, UpdatedAt: now}, nil
}

func validAddress(s string) bool {
	if s == "" || len(s) > 320 || strings.ContainsAny(s, "\r\n\t ,;<>\"") {
		return false
	}
	at := strings.LastIndex(s, "@")
	return at > 0 && at < len(s)-1 && strings.Count(s, "@") == 1
}

// MarkSent records that the SMTP server accepted the message.
func (e *Email) MarkSent(now time.Time) {
	e.Status, e.LastError, e.UpdatedAt = EmailSent, "", now
	e.Attempts++
}

// MarkFailed records a failed attempt; the message is retried by the consumer.
func (e *Email) MarkFailed(cause error, now time.Time) {
	e.Status, e.LastError, e.UpdatedAt = EmailFailed, cause.Error(), now
	e.Attempts++
}

// Mail is a message to hand to the SMTP server.
type Mail struct {
	To      string
	Subject string
	HTML    string
}

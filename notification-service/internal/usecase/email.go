package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// EmailRequest is an email to send from a template.
type EmailRequest struct {
	MessageID string // the Kafka message that caused it: with the template, what makes sending idempotent
	To        string
	Subject   string
	Template  string
	Params    map[string]string
}

// SendEmail renders a template and sends it.
type SendEmail struct{ d *Deps }

// Execute records the email BEFORE the SMTP call, under a key made of the causing message and the template, so a
// redelivered message finds the record: already SENT means nothing to do (no second email); FAILED or PENDING means the
// earlier attempt did not finish and is retried.
//
// Failures are classified for the consumer's retry policy: a bad address, a missing template or a missing placeholder
// value is permanent (dead-letter at once; retrying cannot fix it), while an SMTP error is not (retried with backoff,
// then dead-lettered). Delivery is at-least-once: a crash between the SMTP server accepting the message and the SENT mark
// can send it once more.
func (u SendEmail) Execute(ctx context.Context, r EmailRequest) error {
	return u.d.sendEmail(ctx, r)
}

func (d *Deps) sendEmail(ctx context.Context, r EmailRequest) error {
	if r.MessageID == "" { // the key that makes a redelivery find its record
		return errs.Validation("an email needs the id of the message that caused it")
	}
	now := d.Clock.Now()
	fresh, err := domain.NewEmail(d.IDs.New(), r.MessageID+":"+r.Template, r.To, r.Subject, r.Template, now)
	if err != nil {
		return err
	}
	e, err := d.Emails.Begin(ctx, fresh)
	if err != nil {
		return err
	}
	if e.Status == domain.EmailSent {
		return nil
	}
	body, err := d.render(ctx, r)
	if err != nil {
		e.MarkFailed(err, now)
		if saveErr := d.Emails.Save(ctx, e); saveErr != nil {
			return saveErr
		}
		return err
	}
	if err := d.Sender.Send(ctx, domain.Mail{To: e.To, Subject: e.Subject, HTML: body}); err != nil {
		e.MarkFailed(err, now)
		if saveErr := d.Emails.Save(ctx, e); saveErr != nil {
			return saveErr
		}
		return err
	}
	e.MarkSent(d.Clock.Now())
	return d.Emails.Save(ctx, e)
}

func (d *Deps) render(ctx context.Context, r EmailRequest) (string, error) {
	tpl, err := d.Templates.FindByName(ctx, r.Template)
	if err != nil {
		return "", err
	}
	return domain.Render(tpl.HTML, r.Params, true)
}

// VerificationRequested is auth.email-verification.requested.
type VerificationRequested struct {
	Email     string
	Link      string
	ExpiresAt time.Time
}

// SendVerificationEmail replaces the monolith's RabbitMQ worker.
type SendVerificationEmail struct{ d *Deps }

// Execute sends the confirmation link. A link that has already expired is not sent: the topic is replayed from the
// beginning when this service starts for the first time, and mailing out dead links helps nobody.
func (u SendVerificationEmail) Execute(ctx context.Context, messageID string, ev VerificationRequested) error {
	if !ev.ExpiresAt.IsZero() && !ev.ExpiresAt.After(u.d.Clock.Now()) {
		return nil
	}
	return u.d.sendEmail(ctx, EmailRequest{MessageID: messageID, To: ev.Email, Subject: "Confirm your email", Template: domain.TemplateEmailVerification,
		Params: map[string]string{"link": ev.Link, "expiresAt": ev.ExpiresAt.UTC().Format(time.RFC1123)}})
}

// UserRegistered is auth.user.registered.
type UserRegistered struct{ Email string }

// SendWelcomeEmail greets a new buyer.
type SendWelcomeEmail struct{ d *Deps }

// Execute sends the welcome email.
func (u SendWelcomeEmail) Execute(ctx context.Context, messageID string, ev UserRegistered) error {
	return u.d.sendEmail(ctx, EmailRequest{MessageID: messageID, To: ev.Email, Subject: "Welcome", Template: domain.TemplateWelcome,
		Params: map[string]string{"email": ev.Email}})
}

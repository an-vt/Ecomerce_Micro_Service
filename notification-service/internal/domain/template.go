package domain

import (
	"regexp"
	"strings"
	"time"
)

// Names of the templates the service itself needs.
const (
	TemplateEmailVerification = "email-verification"
	TemplateWelcome           = "welcome"
)

// MaxTemplateBytes bounds a template body.
const MaxTemplateBytes = 100 * 1024

var templateName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{1,63}$`)

// Template is an email body with {{placeholders}}.
type Template struct {
	ID        string
	Name      string
	HTML      string
	Status    string // "active"
	CreatedAt time.Time
}

// NewTemplate validates a template. The name is how emails ask for it, so it is restricted to a safe alphabet.
func NewTemplate(id, name, html string, now time.Time) (*Template, error) {
	name = strings.TrimSpace(name)
	switch {
	case id == "":
		return nil, invalidTemplate("a template needs an id")
	case !templateName.MatchString(name):
		return nil, invalidTemplate("the name must be 2-64 characters: letters, digits, space, '.', '_' or '-'")
	case strings.TrimSpace(html) == "":
		return nil, invalidTemplate("the template body is empty")
	case len(html) > MaxTemplateBytes:
		return nil, invalidTemplate("the template body is larger than 100 KB")
	}
	return &Template{ID: id, Name: name, HTML: html, Status: "active", CreatedAt: now}, nil
}

// DefaultTemplate is a template the service creates for itself when it is missing.
type DefaultTemplate struct{ Name, HTML string }

// DefaultTemplates are the emails the service sends on its own.
func DefaultTemplates() []DefaultTemplate {
	return []DefaultTemplate{
		{TemplateEmailVerification, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Confirm your email</title></head>
<body>
  <h1>Confirm your email</h1>
  <p>Click the link below to finish creating your account. It is valid until {{expiresAt}}.</p>
  <p><a href="{{link}}">{{link}}</a></p>
  <p>If you did not ask for this, you can ignore this message.</p>
</body>
</html>`},
		{TemplateWelcome, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Welcome</title></head>
<body>
  <h1>Welcome!</h1>
  <p>Your account {{email}} is ready. Happy shopping.</p>
</body>
</html>`},
	}
}

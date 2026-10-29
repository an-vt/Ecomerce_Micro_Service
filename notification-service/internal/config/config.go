// Package config loads notification-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	"fmt"
	"net/mail"
	"time"

	platform "github.com/an-vt/ecom/platform-go/config"
)

// Ports of this service.
const (
	DefaultHTTPPort = 8108
	DefaultGRPCPort = 9108
)

// Config of notification-service.
type Config struct {
	platform.Base

	MongoURI    string
	MongoDB     string
	SMTPHost    string
	SMTPPort    int
	SMTPUser    string
	SMTPPass    string // SMTP_PASSWORD, or SMTP_PASSWORD_FILE
	SMTPTimeout time.Duration
	MailFrom    string
	AutoMigrate bool
}

// SMTPAddr is host:port of the relay.
func (c Config) SMTPAddr() string { return fmt.Sprintf("%s:%d", c.SMTPHost, c.SMTPPort) }

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:        platform.LoadBase(l, "notification-service", DefaultHTTPPort, DefaultGRPCPort),
		MongoURI:    l.Required("MONGO_URI"),
		MongoDB:     l.String("MONGO_DB", "notification_db"),
		SMTPHost:    l.String("SMTP_HOST", "ecom-mailpit"),
		SMTPPort:    l.Int("SMTP_PORT", 1025),
		SMTPUser:    l.String("SMTP_USER", ""),
		SMTPPass:    l.String("SMTP_PASSWORD", ""),
		SMTPTimeout: l.DurationMS("SMTP_TIMEOUT_MS", 10*time.Second),
		MailFrom:    l.String("MAIL_FROM", "Ecom <no-reply@ecom.local>"),
		AutoMigrate: l.Bool("AUTO_MIGRATE", true),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	if _, err := mail.ParseAddress(c.MailFrom); err != nil {
		return Config{}, fmt.Errorf("MAIL_FROM: %q is not an email address: %w", c.MailFrom, err)
	}
	return c, nil
}

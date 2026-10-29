package domain

import "time"

// Otp is the one-time token mailed to a buyer to verify an email address (docs/07 §6: 5 minutes, single use).
type Otp struct {
	id        string
	email     Email
	tokenHash string
	expiresAt time.Time
	usedAt    *time.Time
	createdAt time.Time
}

// OtpSnapshot is the persisted form of an Otp.
type OtpSnapshot struct {
	ID        string
	Email     Email
	TokenHash string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// NewOtp creates a token valid for ttl.
func NewOtp(id string, email Email, tokenHash string, now time.Time, ttl time.Duration) *Otp {
	return &Otp{id: id, email: email, tokenHash: tokenHash, expiresAt: now.Add(ttl), createdAt: now}
}

// RestoreOtp rebuilds an Otp from storage.
func RestoreOtp(s OtpSnapshot) *Otp {
	return &Otp{id: s.ID, email: s.Email, tokenHash: s.TokenHash, expiresAt: s.ExpiresAt, usedAt: s.UsedAt, createdAt: s.CreatedAt}
}

// Snapshot returns the persisted state.
func (o *Otp) Snapshot() OtpSnapshot {
	return OtpSnapshot{ID: o.id, Email: o.email, TokenHash: o.tokenHash, ExpiresAt: o.expiresAt, UsedAt: o.usedAt, CreatedAt: o.createdAt}
}

func (o *Otp) ID() string           { return o.id }
func (o *Otp) Email() Email         { return o.email }
func (o *Otp) ExpiresAt() time.Time { return o.expiresAt }
func (o *Otp) Used() bool           { return o.usedAt != nil }

// Consume marks the token used. Unknown, used and expired tokens all fail with the same error.
func (o *Otp) Consume(now time.Time) error {
	if o.usedAt != nil || !now.Before(o.expiresAt) {
		return errTokenInvalid()
	}
	o.usedAt = &now
	return nil
}

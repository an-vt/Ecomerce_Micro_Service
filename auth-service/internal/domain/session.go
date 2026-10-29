package domain

import (
	"slices"
	"time"
)

// Revoke reasons.
const (
	RevokeLogout        = "LOGOUT"
	RevokeReuseDetected = "REUSE_DETECTED"
)

// Session is one login on one device. Its refresh token is opaque; only the SHA-256 hash is kept, together with
// the hashes of the tokens it replaced, which is how reuse of a stolen token is detected (docs/07 §2.1).
type Session struct {
	id           string
	accountID    string
	refreshHash  string
	usedHashes   []string
	expiresAt    time.Time
	revokedAt    *time.Time
	revokeReason string
	createdAt    time.Time
	updatedAt    time.Time
}

// SessionSnapshot is the persisted form of a Session.
type SessionSnapshot struct {
	ID           string
	AccountID    string
	RefreshHash  string
	UsedHashes   []string
	ExpiresAt    time.Time
	RevokedAt    *time.Time
	RevokeReason string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewSession starts a session whose refresh token (hash) is valid for ttl.
func NewSession(id, accountID, refreshHash string, now time.Time, ttl time.Duration) *Session {
	return &Session{id: id, accountID: accountID, refreshHash: refreshHash, expiresAt: now.Add(ttl), createdAt: now, updatedAt: now}
}

// RestoreSession rebuilds a Session from storage.
func RestoreSession(s SessionSnapshot) *Session {
	return &Session{id: s.ID, accountID: s.AccountID, refreshHash: s.RefreshHash, usedHashes: append([]string(nil), s.UsedHashes...),
		expiresAt: s.ExpiresAt, revokedAt: s.RevokedAt, revokeReason: s.RevokeReason, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}
}

// Snapshot returns the persisted state.
func (s *Session) Snapshot() SessionSnapshot {
	return SessionSnapshot{ID: s.id, AccountID: s.accountID, RefreshHash: s.refreshHash, UsedHashes: append([]string(nil), s.usedHashes...),
		ExpiresAt: s.expiresAt, RevokedAt: s.revokedAt, RevokeReason: s.revokeReason, CreatedAt: s.createdAt, UpdatedAt: s.updatedAt}
}

func (s *Session) ID() string           { return s.id }
func (s *Session) AccountID() string    { return s.accountID }
func (s *Session) RefreshHash() string  { return s.refreshHash }
func (s *Session) UsedHashes() []string { return append([]string(nil), s.usedHashes...) }
func (s *Session) ExpiresAt() time.Time { return s.expiresAt }
func (s *Session) Revoked() bool        { return s.revokedAt != nil }
func (s *Session) RevokeReason() string { return s.revokeReason }

// IsActive: not revoked and not expired.
func (s *Session) IsActive(now time.Time) bool { return s.revokedAt == nil && now.Before(s.expiresAt) }

// HasUsed reports whether hash belonged to this session earlier and was rotated away.
func (s *Session) HasUsed(hash string) bool { return slices.Contains(s.usedHashes, hash) }

// Rotate replaces the refresh token by newHash and extends the session by ttl. The old hash is remembered.
// It returns the hash that was replaced (the compare-and-swap value for the repository).
func (s *Session) Rotate(newHash string, now time.Time, ttl time.Duration) (oldHash string, err error) {
	if !s.IsActive(now) {
		return "", errTokenInvalid()
	}
	oldHash = s.refreshHash
	s.usedHashes = append(s.usedHashes, oldHash)
	s.refreshHash = newHash
	s.expiresAt = now.Add(ttl)
	s.updatedAt = now
	return oldHash, nil
}

// Revoke ends the session. Revoking twice keeps the first reason.
func (s *Session) Revoke(reason string, now time.Time) {
	if s.revokedAt != nil {
		return
	}
	s.revokedAt, s.revokeReason, s.updatedAt = &now, reason, now
}

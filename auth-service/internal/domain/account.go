package domain

import (
	"strings"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/principal"
)

// Status of an account.
type Status string

const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusInactive Status = "inactive"
	StatusBlocked  Status = "blocked"
)

// Role names given at registration (docs/07 §4).
const (
	RoleShop = "shop"
	RoleUser = "user"
)

// Account is a shop (seller) or a user (buyer).
type Account struct {
	id           string
	typ          principal.Type
	email        Email
	name         string
	passwordHash string
	status       Status
	verified     bool
	roles        []string
	createdAt    time.Time
	updatedAt    time.Time
}

// AccountSnapshot is the persisted form of an Account.
type AccountSnapshot struct {
	ID           string
	Type         principal.Type
	Email        Email
	Name         string
	PasswordHash string
	Status       Status
	Verified     bool
	Roles        []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewShop registers a seller. The shop is active at once (the monolith left it inactive but never checked).
func NewShop(id, name string, email Email, passwordHash string, now time.Time) (*Account, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 150 {
		return nil, errs.Validation("name is required and must be at most 150 characters")
	}
	if passwordHash == "" {
		return nil, errs.Validation("password hash is required")
	}
	return &Account{id: id, typ: principal.TypeShop, email: email, name: name, passwordHash: passwordHash,
		status: StatusActive, roles: []string{RoleShop}, createdAt: now, updatedAt: now}, nil
}

// NewUser creates a buyer whose email was just verified. It has no password until SetPasswordHash is called.
func NewUser(id string, email Email, now time.Time) *Account {
	return &Account{id: id, typ: principal.TypeUser, email: email, name: email.LocalPart(), status: StatusActive,
		verified: true, roles: []string{RoleUser}, createdAt: now, updatedAt: now}
}

// RestoreAccount rebuilds an Account from storage.
func RestoreAccount(s AccountSnapshot) *Account {
	return &Account{id: s.ID, typ: s.Type, email: s.Email, name: s.Name, passwordHash: s.PasswordHash, status: s.Status,
		verified: s.Verified, roles: append([]string(nil), s.Roles...), createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}
}

// Snapshot returns the persisted state.
func (a *Account) Snapshot() AccountSnapshot {
	return AccountSnapshot{ID: a.id, Type: a.typ, Email: a.email, Name: a.name, PasswordHash: a.passwordHash, Status: a.status,
		Verified: a.verified, Roles: append([]string(nil), a.roles...), CreatedAt: a.createdAt, UpdatedAt: a.updatedAt}
}

func (a *Account) ID() string           { return a.id }
func (a *Account) Type() principal.Type { return a.typ }
func (a *Account) Email() Email         { return a.email }
func (a *Account) Name() string         { return a.name }
func (a *Account) PasswordHash() string { return a.passwordHash }
func (a *Account) Status() Status       { return a.status }
func (a *Account) Verified() bool       { return a.verified }
func (a *Account) Roles() []string      { return append([]string(nil), a.roles...) }
func (a *Account) HasPassword() bool    { return a.passwordHash != "" }

// CheckLogin says whether the account may start a session.
func (a *Account) CheckLogin() error {
	if a.status != StatusActive {
		return errAccountInactive()
	}
	if !a.HasPassword() {
		return errPasswordNotSet()
	}
	return nil
}

// SetPasswordHash replaces the password.
func (a *Account) SetPasswordHash(hash string, now time.Time) error {
	if hash == "" {
		return errs.Validation("password hash is required")
	}
	a.passwordHash, a.updatedAt = hash, now
	return nil
}

// IsActive reports whether the account may hold sessions (used when refreshing a token).
func (a *Account) IsActive() bool { return a.status == StatusActive }

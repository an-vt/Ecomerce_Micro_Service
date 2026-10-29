package domain

import (
	"net/mail"
	"strings"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Email is a normalised (trimmed, lower-case) and validated address.
type Email string

// ParseEmail validates s. Only the bare address form is accepted ("a@b.c", not "Name <a@b.c>").
func ParseEmail(s string) (Email, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 320 {
		return "", errs.InvalidArgument(CodeInvalidEmail, "email is required and must be at most 320 characters")
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || !strings.Contains(s[strings.LastIndex(s, "@"):], ".") {
		return "", errs.InvalidArgument(CodeInvalidEmail, "email is not a valid address")
	}
	return Email(s), nil
}

// String returns the address.
func (e Email) String() string { return string(e) }

// LocalPart is the part before the @ (used as a default display name).
func (e Email) LocalPart() string { return string(e)[:strings.LastIndex(string(e), "@")] }

// ValidatePassword enforces the password policy before hashing.
func ValidatePassword(p string) error {
	if len(p) < 8 {
		return errs.InvalidArgument(CodeWeakPassword, "password must be at least 8 characters")
	}
	if len(p) > 72 { // bcrypt ignores everything after 72 bytes
		return errs.InvalidArgument(CodeWeakPassword, "password must be at most 72 characters")
	}
	return nil
}

package domain

import "slices"

// PermissionAll is the permission the gateway requires on every public route (the monolith's '0000').
const PermissionAll = "0000"

// ApiKey authorises a client application. Only the SHA-256 hash of the key is stored.
type ApiKey struct {
	ID          string
	Name        string
	KeyHash     string
	Permissions []string
	Active      bool
}

// Allows reports whether the key is active and holds permission p.
func (k ApiKey) Allows(p string) bool { return k.Active && slices.Contains(k.Permissions, p) }

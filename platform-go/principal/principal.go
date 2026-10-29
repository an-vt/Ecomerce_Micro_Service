// Package principal carries the authenticated caller through a request (docs/07-auth-security.md §4–5).
// The gateway verifies the token and forwards the principal in gRPC metadata; services trust it because only the
// gateway is exposed.
package principal

import "context"

// Type of account.
type Type string

const (
	TypeShop Type = "SHOP"
	TypeUser Type = "USER"
)

// Principal is the caller.
type Principal struct {
	ID        string
	Type      Type
	Roles     []string
	SessionID string
}

// IsShop / IsUser / HasRole are convenience checks.
func (p Principal) IsShop() bool { return p.Type == TypeShop }
func (p Principal) IsUser() bool { return p.Type == TypeUser }
func (p Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// With returns ctx carrying p.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal in ctx.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok && p.ID != ""
}

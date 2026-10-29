package domain

import (
	"regexp"
	"slices"
	"strings"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Role statuses.
const (
	RoleActive  = "active"
	RoleBlocked = "block"
	RolePending = "pending"
)

// ValidActions are the accesscontrol actions a grant may contain (docs/07 §4).
var ValidActions = []string{
	"createAny", "readAny", "updateAny", "deleteAny",
	"createOwn", "readOwn", "updateOwn", "deleteOwn",
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// Grant lets a role perform actions on a resource (by name).
type Grant struct {
	Resource   string
	Actions    []string
	Attributes string
}

// Role groups grants.
type Role struct {
	ID          string
	Name        string
	Slug        string
	Status      string
	Description string
	Grants      []Grant
}

// NewRole validates a role definition.
func NewRole(id, name, slug, status, description string, grants []Grant) (*Role, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !nameRe.MatchString(name) {
		return nil, errs.Validation("role name must be 2-32 characters: lower-case letters, digits, '-' or '_'")
	}
	if slug == "" {
		slug = name
	}
	if status == "" {
		status = RoleActive
	}
	if !slices.Contains([]string{RoleActive, RoleBlocked, RolePending}, status) {
		return nil, errs.Validation("role status must be active, block or pending")
	}
	seen := map[string]bool{}
	out := make([]Grant, 0, len(grants))
	for _, g := range grants {
		g.Resource = strings.ToLower(strings.TrimSpace(g.Resource))
		if g.Resource == "" || seen[g.Resource] {
			return nil, errs.Validation("every grant needs a distinct resource")
		}
		seen[g.Resource] = true
		if len(g.Actions) == 0 {
			return nil, errs.Validation("grant for " + g.Resource + " has no actions")
		}
		for _, a := range g.Actions {
			if !slices.Contains(ValidActions, a) {
				return nil, errs.Validation("unknown action " + a)
			}
		}
		if g.Attributes == "" {
			g.Attributes = "*"
		}
		g.Actions = slices.Clone(g.Actions)
		out = append(out, g)
	}
	// A canonical order makes a role compare equal after any round trip through storage.
	slices.SortFunc(out, func(a, b Grant) int { return strings.Compare(a.Resource, b.Resource) })
	return &Role{ID: id, Name: name, Slug: slug, Status: status, Description: description, Grants: out}, nil
}

// Resource is something a grant can refer to (profile, product, rbac, …).
type Resource struct {
	ID          string
	Name        string
	Slug        string
	Description string
}

// NewResource validates a resource definition.
func NewResource(id, name, slug, description string) (*Resource, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !nameRe.MatchString(name) {
		return nil, errs.Validation("resource name must be 2-32 characters: lower-case letters, digits, '-' or '_'")
	}
	if slug == "" {
		slug = name
	}
	return &Resource{ID: id, Name: name, Slug: slug, Description: description}, nil
}

// RoleGrant is one flattened grant, as served to the gateway.
type RoleGrant struct {
	Role       string
	Resource   string
	Actions    []string
	Attributes string
}

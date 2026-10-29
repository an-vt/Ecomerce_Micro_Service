package usecase

import (
	"context"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// CreateRoleInput defines a role with its grants.
type CreateRoleInput struct {
	Name, Slug, Status, Description string
	Grants                          []domain.Grant
}

// CreateRole adds a role (monolith RbacService.createRole).
type CreateRole struct{ d *Deps }

// Execute validates the role and checks that every granted resource exists.
func (u CreateRole) Execute(ctx context.Context, in CreateRoleInput) (*domain.Role, error) {
	role, err := domain.NewRole(u.d.IDs.New(), in.Name, in.Slug, in.Status, in.Description, in.Grants)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(role.Grants))
	for i, g := range role.Grants {
		names[i] = g.Resource
	}
	missing, err := u.d.Rbac.MissingResources(ctx, names)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, errs.NotFound(domain.CodeResourceNotFound, "unknown resource: "+missing[0])
	}
	if err := u.d.Rbac.CreateRole(ctx, role); err != nil {
		return nil, err
	}
	return role, nil
}

// ListRoles returns all roles with their grants.
type ListRoles struct{ d *Deps }

// Execute lists the roles.
func (u ListRoles) Execute(ctx context.Context) ([]domain.Role, error) {
	return u.d.Rbac.ListRoles(ctx)
}

// CreateResourceInput defines a resource.
type CreateResourceInput struct{ Name, Slug, Description string }

// CreateResource adds a resource (monolith RbacService.createResource).
type CreateResource struct{ d *Deps }

// Execute validates and stores the resource.
func (u CreateResource) Execute(ctx context.Context, in CreateResourceInput) (*domain.Resource, error) {
	res, err := domain.NewResource(u.d.IDs.New(), in.Name, in.Slug, in.Description)
	if err != nil {
		return nil, err
	}
	if err := u.d.Rbac.CreateResource(ctx, res); err != nil {
		return nil, err
	}
	return res, nil
}

// ListResources returns all resources.
type ListResources struct{ d *Deps }

// Execute lists the resources.
func (u ListResources) Execute(ctx context.Context) ([]domain.Resource, error) {
	return u.d.Rbac.ListResources(ctx)
}

// GetGrants returns the flattened grants of all active roles for the gateway.
type GetGrants struct{ d *Deps }

// Execute lists the grants.
func (u GetGrants) Execute(ctx context.Context) ([]domain.RoleGrant, error) {
	return u.d.Rbac.ActiveGrants(ctx)
}

package postgres

import (
	"context"
	"sort"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type roleRow struct {
	bun.BaseModel `bun:"table:roles"`

	ID          string    `bun:"id,pk"`
	Name        string    `bun:"name"`
	Slug        string    `bun:"slug"`
	Status      string    `bun:"status"`
	Description string    `bun:"description"`
	CreatedAt   time.Time `bun:"created_at,default:now()"`
	UpdatedAt   time.Time `bun:"updated_at,default:now()"`
}

type resourceRow struct {
	bun.BaseModel `bun:"table:resources"`

	ID          string    `bun:"id,pk"`
	Name        string    `bun:"name"`
	Slug        string    `bun:"slug"`
	Description string    `bun:"description"`
	CreatedAt   time.Time `bun:"created_at,default:now()"`
}

type grantRow struct {
	bun.BaseModel `bun:"table:grants"`

	ID         string   `bun:"id,pk"`
	RoleID     string   `bun:"role_id"`
	ResourceID string   `bun:"resource_id"`
	Actions    []string `bun:"actions,array"`
	Attributes string   `bun:"attributes"`
}

type rbacRepo struct {
	s  *pgdb.Store
	tx *pgdb.TxManager
}

func (r *rbacRepo) resourceIDs(ctx context.Context, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	var rows []resourceRow
	if err := r.s.DB(ctx).NewSelect().Model(&rows).Column("id", "name").Where("name IN (?)", bun.List(names)).Scan(ctx); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.Name] = row.ID
	}
	return out, nil
}

// writeGrants replaces the grants of a role.
func (r *rbacRepo) writeGrants(ctx context.Context, roleID string, grants []domain.Grant) error {
	if _, err := r.s.DB(ctx).NewDelete().Model((*grantRow)(nil)).Where("role_id = ?", roleID).Exec(ctx); err != nil {
		return err
	}
	if len(grants) == 0 {
		return nil
	}
	names := make([]string, len(grants))
	for i, g := range grants {
		names[i] = g.Resource
	}
	ids, err := r.resourceIDs(ctx, names)
	if err != nil {
		return err
	}
	rows := make([]grantRow, len(grants))
	for i, g := range grants {
		rid, ok := ids[g.Resource]
		if !ok {
			return errs.NotFound(domain.CodeResourceNotFound, "unknown resource: "+g.Resource)
		}
		rows[i] = grantRow{ID: idgen.UUID{}.New(), RoleID: roleID, ResourceID: rid, Actions: g.Actions, Attributes: g.Attributes}
	}
	_, err = r.s.DB(ctx).NewInsert().Model(&rows).Exec(ctx)
	return err
}

func (r *rbacRepo) CreateRole(ctx context.Context, role *domain.Role) error {
	return r.tx.WithinTx(ctx, func(ctx context.Context) error {
		_, err := r.s.DB(ctx).NewInsert().Model(&roleRow{ID: role.ID, Name: role.Name, Slug: role.Slug, Status: role.Status, Description: role.Description}).Exec(ctx)
		if pgdb.IsUniqueViolation(err, "uq_roles_name") {
			return errs.AlreadyExists(domain.CodeRoleExists, "role "+role.Name+" already exists")
		}
		if err != nil {
			return err
		}
		return r.writeGrants(ctx, role.ID, role.Grants)
	})
}

func (r *rbacRepo) UpsertRole(ctx context.Context, role *domain.Role) error {
	return r.tx.WithinTx(ctx, func(ctx context.Context) error {
		var id string
		err := r.s.DB(ctx).NewRaw(`INSERT INTO roles (id, name, slug, status, description) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (name) DO UPDATE SET slug = EXCLUDED.slug, status = EXCLUDED.status, description = EXCLUDED.description, updated_at = now()
			RETURNING id`, role.ID, role.Name, role.Slug, role.Status, role.Description).Scan(ctx, &id)
		if err != nil {
			return err
		}
		role.ID = id
		return r.writeGrants(ctx, id, role.Grants)
	})
}

func (r *rbacRepo) ListRoles(ctx context.Context) ([]domain.Role, error) {
	var roles []roleRow
	if err := r.s.DB(ctx).NewSelect().Model(&roles).OrderExpr("name").Scan(ctx); err != nil {
		return nil, err
	}
	type grantJoin struct {
		RoleID     string   `bun:"role_id"`
		Resource   string   `bun:"resource"`
		Actions    []string `bun:"actions,array"`
		Attributes string   `bun:"attributes"`
	}
	var gs []grantJoin
	if err := r.s.DB(ctx).NewSelect().TableExpr("grants AS g").
		ColumnExpr("g.role_id, r.name AS resource, g.actions, g.attributes").
		Join("JOIN resources AS r ON r.id = g.resource_id").OrderExpr("r.name").Scan(ctx, &gs); err != nil {
		return nil, err
	}
	byRole := map[string][]domain.Grant{}
	for _, g := range gs {
		byRole[g.RoleID] = append(byRole[g.RoleID], domain.Grant{Resource: g.Resource, Actions: g.Actions, Attributes: g.Attributes})
	}
	out := make([]domain.Role, len(roles))
	for i, row := range roles {
		out[i] = domain.Role{ID: row.ID, Name: row.Name, Slug: row.Slug, Status: row.Status, Description: row.Description, Grants: byRole[row.ID]}
	}
	return out, nil
}

func (r *rbacRepo) CreateResource(ctx context.Context, res *domain.Resource) error {
	_, err := r.s.DB(ctx).NewInsert().Model(&resourceRow{ID: res.ID, Name: res.Name, Slug: res.Slug, Description: res.Description}).Exec(ctx)
	if pgdb.IsUniqueViolation(err, "uq_resources_name") {
		return errs.AlreadyExists(domain.CodeResourceExists, "resource "+res.Name+" already exists")
	}
	return err
}

func (r *rbacRepo) UpsertResource(ctx context.Context, res *domain.Resource) error {
	var id string
	err := r.s.DB(ctx).NewRaw(`INSERT INTO resources (id, name, slug, description) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET slug = EXCLUDED.slug, description = EXCLUDED.description RETURNING id`,
		res.ID, res.Name, res.Slug, res.Description).Scan(ctx, &id)
	if err == nil {
		res.ID = id
	}
	return err
}

func (r *rbacRepo) ListResources(ctx context.Context) ([]domain.Resource, error) {
	var rows []resourceRow
	if err := r.s.DB(ctx).NewSelect().Model(&rows).OrderExpr("name").Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]domain.Resource, len(rows))
	for i, row := range rows {
		out[i] = domain.Resource{ID: row.ID, Name: row.Name, Slug: row.Slug, Description: row.Description}
	}
	return out, nil
}

func (r *rbacRepo) MissingResources(ctx context.Context, names []string) ([]string, error) {
	ids, err := r.resourceIDs(ctx, names)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, n := range names {
		if _, ok := ids[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing, nil
}

func (r *rbacRepo) ActiveGrants(ctx context.Context) ([]domain.RoleGrant, error) {
	var rows []struct {
		Role       string   `bun:"role"`
		Resource   string   `bun:"resource"`
		Actions    []string `bun:"actions,array"`
		Attributes string   `bun:"attributes"`
	}
	err := r.s.DB(ctx).NewSelect().TableExpr("grants AS g").
		ColumnExpr("ro.name AS role, re.name AS resource, g.actions, g.attributes").
		Join("JOIN roles AS ro ON ro.id = g.role_id").Join("JOIN resources AS re ON re.id = g.resource_id").
		Where("ro.status = 'active'").Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RoleGrant, len(rows))
	for i, row := range rows {
		out[i] = domain.RoleGrant{Role: row.Role, Resource: row.Resource, Actions: row.Actions, Attributes: row.Attributes}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		return out[i].Resource < out[j].Resource
	})
	return out, nil
}

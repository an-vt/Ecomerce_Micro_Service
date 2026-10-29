package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/emptypb"

	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
)

// Rbac holds the role / resource administration routes (admin only).
type Rbac struct{ client authv1.RbacServiceClient }

type grantJSON struct {
	Resource   string   `json:"resource" binding:"required"`
	Actions    []string `json:"actions" binding:"required"`
	Attributes string   `json:"attributes"`
}

type roleJSON struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Slug        string      `json:"slug"`
	Status      string      `json:"status"`
	Description string      `json:"description"`
	Grants      []grantJSON `json:"grants"`
}

type resourceJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

func roleOf(r *authv1.Role) roleJSON {
	out := roleJSON{ID: r.GetId(), Name: r.GetName(), Slug: r.GetSlug(), Status: r.GetStatus(), Description: r.GetDescription(), Grants: []grantJSON{}}
	for _, g := range r.GetGrants() {
		out.Grants = append(out.Grants, grantJSON{Resource: g.GetResource(), Actions: g.GetActions(), Attributes: g.GetAttributes()})
	}
	return out
}

func resourceOf(r *authv1.Resource) resourceJSON {
	return resourceJSON{ID: r.GetId(), Name: r.GetName(), Slug: r.GetSlug(), Description: r.GetDescription()}
}

func (h *Rbac) createRole(c *gin.Context) {
	var in struct {
		Name        string      `json:"name" binding:"required"`
		Slug        string      `json:"slug"`
		Status      string      `json:"status"`
		Description string      `json:"description"`
		Grants      []grantJSON `json:"grants" binding:"dive"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "name is required and every grant needs a resource and actions")
		return
	}
	req := &authv1.CreateRoleRequest{Name: in.Name, Slug: in.Slug, Status: in.Status, Description: in.Description}
	for _, g := range in.Grants {
		req.Grants = append(req.Grants, &authv1.Grant{Resource: g.Resource, Actions: g.Actions, Attributes: g.Attributes})
	}
	r, err := h.client.CreateRole(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusCreated, "Created role", roleOf(r))
}

func (h *Rbac) listRoles(c *gin.Context) {
	r, err := h.client.ListRoles(c.Request.Context(), &emptypb.Empty{})
	if err != nil {
		fail(c, err)
		return
	}
	roles := make([]roleJSON, 0, len(r.GetRoles()))
	for _, role := range r.GetRoles() {
		roles = append(roles, roleOf(role))
	}
	ok(c, stdhttp.StatusOK, "Roles", roles)
}

func (h *Rbac) createResource(c *gin.Context) {
	var in struct {
		Name        string `json:"name" binding:"required"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "name is required")
		return
	}
	r, err := h.client.CreateResource(c.Request.Context(), &authv1.CreateResourceRequest{Name: in.Name, Slug: in.Slug, Description: in.Description})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusCreated, "Created resource", resourceOf(r))
}

func (h *Rbac) listResources(c *gin.Context) {
	r, err := h.client.ListResources(c.Request.Context(), &emptypb.Empty{})
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]resourceJSON, 0, len(r.GetResources()))
	for _, res := range r.GetResources() {
		out = append(out, resourceOf(res))
	}
	ok(c, stdhttp.StatusOK, "Resources", out)
}

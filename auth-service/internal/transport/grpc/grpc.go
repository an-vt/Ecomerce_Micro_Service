// Package grpc exposes the use cases as the AuthService and RbacService gRPC services (proto/auth/v1). Handlers
// translate between protobuf messages and use-case inputs and nothing else: business rules live in usecase/ and
// domain/, error mapping in platform-go/grpcx.
package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/auth-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/principal"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
)

// AuthServer implements authv1.AuthServiceServer.
type AuthServer struct {
	authv1.UnimplementedAuthServiceServer
	uc *usecase.Set
}

// NewAuthServer creates the handler.
func NewAuthServer(uc *usecase.Set) *AuthServer { return &AuthServer{uc: uc} }

func accountType(t authv1.AccountType) (principal.Type, error) {
	switch t {
	case authv1.AccountType_ACCOUNT_TYPE_SHOP:
		return principal.TypeShop, nil
	case authv1.AccountType_ACCOUNT_TYPE_USER:
		return principal.TypeUser, nil
	}
	return "", errs.Validation("account type must be SHOP or USER")
}

func toAccountType(t string) authv1.AccountType {
	switch principal.Type(t) {
	case principal.TypeShop:
		return authv1.AccountType_ACCOUNT_TYPE_SHOP
	case principal.TypeUser:
		return authv1.AccountType_ACCOUNT_TYPE_USER
	}
	return authv1.AccountType_ACCOUNT_TYPE_UNSPECIFIED
}

func toAccount(v usecase.AccountView) *authv1.Account {
	return &authv1.Account{Id: v.ID, Type: toAccountType(v.Type), Email: v.Email, Name: v.Name, Status: v.Status,
		Verified: v.Verified, Roles: v.Roles, HasPassword: v.HasPassword}
}

func toAuthResponse(r usecase.AuthResult) *authv1.AuthResponse {
	return &authv1.AuthResponse{
		Account: toAccount(r.Account),
		Tokens: &authv1.Tokens{AccessToken: r.Tokens.AccessToken, RefreshToken: r.Tokens.RefreshToken,
			ExpiresInSeconds: int64(r.Tokens.ExpiresIn.Seconds()), SessionId: r.Tokens.SessionID},
	}
}

func (s *AuthServer) SignUpShop(ctx context.Context, in *authv1.SignUpShopRequest) (*authv1.AuthResponse, error) {
	r, err := s.uc.SignUpShop.Execute(ctx, usecase.SignUpShopInput{Name: in.GetName(), Email: in.GetEmail(), Password: in.GetPassword()})
	if err != nil {
		return nil, err
	}
	return toAuthResponse(r), nil
}

func (s *AuthServer) Login(ctx context.Context, in *authv1.LoginRequest) (*authv1.AuthResponse, error) {
	typ, err := accountType(in.GetType())
	if err != nil {
		return nil, err
	}
	r, err := s.uc.Login.Execute(ctx, usecase.LoginInput{Type: typ, Email: in.GetEmail(), Password: in.GetPassword()})
	if err != nil {
		return nil, err
	}
	return toAuthResponse(r), nil
}

func (s *AuthServer) Refresh(ctx context.Context, in *authv1.RefreshRequest) (*authv1.AuthResponse, error) {
	r, err := s.uc.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: in.GetRefreshToken()})
	if err != nil {
		return nil, err
	}
	return toAuthResponse(r), nil
}

// caller returns the authenticated principal forwarded by the gateway.
func caller(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.From(ctx)
	if !ok {
		return principal.Principal{}, errs.Unauthenticated("UNAUTHENTICATED", "this call needs an authenticated caller")
	}
	return p, nil
}

func (s *AuthServer) Logout(ctx context.Context, in *authv1.LogoutRequest) (*emptypb.Empty, error) {
	p, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	sid := in.GetSessionId()
	if sid == "" {
		sid = p.SessionID
	}
	if err := s.uc.Logout.Execute(ctx, usecase.LogoutInput{AccountID: p.ID, SessionID: sid}); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *AuthServer) RequestEmailVerification(ctx context.Context, in *authv1.RequestEmailVerificationRequest) (*emptypb.Empty, error) {
	if err := s.uc.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: in.GetEmail()}); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *AuthServer) VerifyEmail(ctx context.Context, in *authv1.VerifyEmailRequest) (*authv1.AuthResponse, error) {
	r, err := s.uc.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: in.GetToken()})
	if err != nil {
		return nil, err
	}
	return toAuthResponse(r), nil
}

func (s *AuthServer) SetPassword(ctx context.Context, in *authv1.SetPasswordRequest) (*emptypb.Empty, error) {
	p, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.uc.SetPassword.Execute(ctx, usecase.SetPasswordInput{AccountID: p.ID, Password: in.GetPassword()}); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// GetAccount returns the requested account; callers may only read their own unless they hold the admin role.
func (s *AuthServer) GetAccount(ctx context.Context, in *authv1.GetAccountRequest) (*authv1.Account, error) {
	p, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	id := in.GetAccountId()
	if id == "" {
		id = p.ID
	}
	if id != p.ID && !p.HasRole("admin") {
		return nil, errs.PermissionDenied("FORBIDDEN", "you can only read your own account")
	}
	v, err := s.uc.GetAccount.Execute(ctx, id)
	if err != nil {
		return nil, err
	}
	return toAccount(v), nil
}

func (s *AuthServer) ValidateApiKey(ctx context.Context, in *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
	r, err := s.uc.ValidateApiKey.Execute(ctx, in.GetKey())
	if err != nil {
		return nil, err
	}
	return &authv1.ValidateApiKeyResponse{Valid: r.Valid, Permissions: r.Permissions}, nil
}

func (s *AuthServer) GetJwks(context.Context, *emptypb.Empty) (*authv1.JwksResponse, error) {
	set := s.uc.GetJwks.Execute()
	out := &authv1.JwksResponse{}
	for _, k := range set.Keys {
		out.Keys = append(out.Keys, &authv1.Jwk{Kty: k.Kty, Kid: k.Kid, Alg: k.Alg, Use: k.Use, N: k.N, E: k.E})
	}
	return out, nil
}

// ---- RBAC -----------------------------------------------------------------------------------------------------

// RbacServer implements authv1.RbacServiceServer. The gateway guards these routes with the admin role.
type RbacServer struct {
	authv1.UnimplementedRbacServiceServer
	uc *usecase.Set
}

// NewRbacServer creates the handler.
func NewRbacServer(uc *usecase.Set) *RbacServer { return &RbacServer{uc: uc} }

func toRole(r domain.Role) *authv1.Role {
	out := &authv1.Role{Id: r.ID, Name: r.Name, Slug: r.Slug, Status: r.Status, Description: r.Description}
	for _, g := range r.Grants {
		out.Grants = append(out.Grants, &authv1.Grant{Resource: g.Resource, Actions: g.Actions, Attributes: g.Attributes})
	}
	return out
}

func (s *RbacServer) CreateRole(ctx context.Context, in *authv1.CreateRoleRequest) (*authv1.Role, error) {
	grants := make([]domain.Grant, len(in.GetGrants()))
	for i, g := range in.GetGrants() {
		grants[i] = domain.Grant{Resource: g.GetResource(), Actions: g.GetActions(), Attributes: g.GetAttributes()}
	}
	r, err := s.uc.CreateRole.Execute(ctx, usecase.CreateRoleInput{Name: in.GetName(), Slug: in.GetSlug(), Status: in.GetStatus(),
		Description: in.GetDescription(), Grants: grants})
	if err != nil {
		return nil, err
	}
	return toRole(*r), nil
}

func (s *RbacServer) ListRoles(ctx context.Context, _ *emptypb.Empty) (*authv1.ListRolesResponse, error) {
	roles, err := s.uc.ListRoles.Execute(ctx)
	if err != nil {
		return nil, err
	}
	out := &authv1.ListRolesResponse{}
	for _, r := range roles {
		out.Roles = append(out.Roles, toRole(r))
	}
	return out, nil
}

func toResource(r domain.Resource) *authv1.Resource {
	return &authv1.Resource{Id: r.ID, Name: r.Name, Slug: r.Slug, Description: r.Description}
}

func (s *RbacServer) CreateResource(ctx context.Context, in *authv1.CreateResourceRequest) (*authv1.Resource, error) {
	r, err := s.uc.CreateResource.Execute(ctx, usecase.CreateResourceInput{Name: in.GetName(), Slug: in.GetSlug(), Description: in.GetDescription()})
	if err != nil {
		return nil, err
	}
	return toResource(*r), nil
}

func (s *RbacServer) ListResources(ctx context.Context, _ *emptypb.Empty) (*authv1.ListResourcesResponse, error) {
	rs, err := s.uc.ListResources.Execute(ctx)
	if err != nil {
		return nil, err
	}
	out := &authv1.ListResourcesResponse{}
	for _, r := range rs {
		out.Resources = append(out.Resources, toResource(r))
	}
	return out, nil
}

func (s *RbacServer) GetGrants(ctx context.Context, _ *emptypb.Empty) (*authv1.GetGrantsResponse, error) {
	gs, err := s.uc.GetGrants.Execute(ctx)
	if err != nil {
		return nil, err
	}
	out := &authv1.GetGrantsResponse{}
	for _, g := range gs {
		out.Grants = append(out.Grants, &authv1.RoleGrant{Role: g.Role, Resource: g.Resource, Actions: g.Actions, Attributes: g.Attributes})
	}
	return out, nil
}

package grpc_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/auth-service/internal/fake"
	transport "github.com/an-vt/ecom/auth-service/internal/transport/grpc"
	"github.com/an-vt/ecom/auth-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/jwtx"
	"github.com/an-vt/ecom/platform-go/metrics"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
)

type env struct {
	auth authv1.AuthServiceClient
	rbac authv1.RbacServiceClient
	w    *fake.World
	clk  *clock.Fixed
}

// start runs the real gRPC server (with the production interceptor chain) over an in-process connection, backed by
// real use cases on the in-memory fakes.
func start(t *testing.T) *env {
	t.Helper()
	clk := clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	w := fake.NewWorld()
	uc := usecase.NewSet(&usecase.Deps{
		Accounts: w.Accounts(), Sessions: w.Sessions(), Otps: w.Otps(), ApiKeys: w.ApiKeys(), Rbac: w.Rbac(),
		Hasher: &fake.Hasher{}, Signer: fake.NewSigner(15*time.Minute, clk), Secrets: &fake.Secrets{}, Denylist: w, Tx: w, Outbox: w,
		Clock: clk, IDs: &idgen.Sequence{Prefix: "id-"},
		Settings: usecase.Settings{RefreshTTL: time.Hour, OTPTTL: 5 * time.Minute, VerifyLinkBase: "http://x/verify"},
	})
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer("auth-service", metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	authv1.RegisterAuthServiceServer(srv, transport.NewAuthServer(uc))
	authv1.RegisterRbacServiceServer(srv, transport.NewRbacServer(uc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return grpcx.FromStatus(inv(ctx, m, req, reply, cc, o...))
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return &env{auth: authv1.NewAuthServiceClient(conn), rbac: authv1.NewRbacServiceClient(conn), w: w, clk: clk}
}

// as returns a context carrying the principal metadata the gateway would forward.
func as(id, typ, roles, sid string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		grpcx.MDAccountID, id, grpcx.MDAccountType, typ, grpcx.MDRoles, roles, grpcx.MDSessionID, sid)
}

var bg = context.Background()

func TestShopLifecycleOverGRPC(t *testing.T) {
	e := start(t)
	signed, err := e.auth.SignUpShop(bg, &authv1.SignUpShopRequest{Name: "Acme", Email: "owner@acme.com", Password: "correct-horse"})
	require.NoError(t, err)
	assert.Equal(t, authv1.AccountType_ACCOUNT_TYPE_SHOP, signed.GetAccount().GetType())
	assert.Equal(t, []string{"shop"}, signed.GetAccount().GetRoles())
	assert.True(t, signed.GetAccount().GetHasPassword())
	assert.Equal(t, int64(900), signed.GetTokens().GetExpiresInSeconds())
	assert.NotEmpty(t, signed.GetTokens().GetSessionId())

	// the published JWKS verifies the token the service issued
	jwks, err := e.auth.GetJwks(bg, &emptypb.Empty{})
	require.NoError(t, err)
	set := jwtx.JWKS{}
	for _, k := range jwks.GetKeys() {
		set.Keys = append(set.Keys, jwtx.JWK{Kty: k.GetKty(), Kid: k.GetKid(), Alg: k.GetAlg(), Use: k.GetUse(), N: k.GetN(), E: k.GetE()})
	}
	v := jwtx.NewVerifier("ecom-auth", "ecom", e.clk, nil)
	require.NoError(t, v.SetKeys(set))
	claims, err := v.Verify(bg, signed.GetTokens().GetAccessToken())
	require.NoError(t, err)
	assert.Equal(t, signed.GetAccount().GetId(), claims.AccountID())

	login, err := e.auth.Login(bg, &authv1.LoginRequest{Type: authv1.AccountType_ACCOUNT_TYPE_SHOP, Email: "owner@acme.com", Password: "correct-horse"})
	require.NoError(t, err)

	refreshed, err := e.auth.Refresh(bg, &authv1.RefreshRequest{RefreshToken: login.GetTokens().GetRefreshToken()})
	require.NoError(t, err)
	assert.NotEqual(t, login.GetTokens().GetRefreshToken(), refreshed.GetTokens().GetRefreshToken())

	_, err = e.auth.Refresh(bg, &authv1.RefreshRequest{RefreshToken: login.GetTokens().GetRefreshToken()})
	require.Error(t, err)
	assert.Equal(t, domain.CodeTokenReused, errs.CodeOf(err), "the domain code crosses the wire")
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
}

func TestErrorsKeepTheirKindAndCodeAcrossTheWire(t *testing.T) {
	e := start(t)
	_, err := e.auth.SignUpShop(bg, &authv1.SignUpShopRequest{Name: "A", Email: "a@acme.com", Password: "correct-horse"})
	require.NoError(t, err)

	_, err = e.auth.SignUpShop(bg, &authv1.SignUpShopRequest{Name: "B", Email: "a@acme.com", Password: "correct-horse"})
	assert.Equal(t, domain.CodeEmailTaken, errs.CodeOf(err))
	assert.Equal(t, errs.KindAlreadyExists, errs.KindOf(err))

	_, err = e.auth.Login(bg, &authv1.LoginRequest{Type: authv1.AccountType_ACCOUNT_TYPE_SHOP, Email: "a@acme.com", Password: "wrong-password"})
	assert.Equal(t, domain.CodeInvalidCredentials, errs.CodeOf(err))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))

	_, err = e.auth.Login(bg, &authv1.LoginRequest{Email: "a@acme.com", Password: "x"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err), "unspecified account type")

	_, err = e.auth.SignUpShop(bg, &authv1.SignUpShopRequest{Name: "B", Email: "b@acme.com", Password: "short"})
	assert.Equal(t, domain.CodeWeakPassword, errs.CodeOf(err))
}

func TestBuyerFlowOverGRPC(t *testing.T) {
	e := start(t)
	_, err := e.auth.RequestEmailVerification(bg, &authv1.RequestEmailVerificationRequest{Email: "buyer@example.com"})
	require.NoError(t, err)

	envl, err := event.Parse(e.w.Outbox[0].Payload)
	require.NoError(t, err)
	var ev domain.EmailVerificationRequested
	require.NoError(t, envl.Decode(&ev))
	u, err := url.Parse(ev.Link)
	require.NoError(t, err)

	verified, err := e.auth.VerifyEmail(bg, &authv1.VerifyEmailRequest{Token: u.Query().Get("token")})
	require.NoError(t, err)
	assert.Equal(t, authv1.AccountType_ACCOUNT_TYPE_USER, verified.GetAccount().GetType())
	assert.False(t, verified.GetAccount().GetHasPassword())
	id := verified.GetAccount().GetId()

	// SetPassword uses the principal forwarded by the gateway, never an id from the request
	_, err = e.auth.SetPassword(bg, &authv1.SetPasswordRequest{Password: "my-new-password"})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err), "no principal metadata: refused")
	_, err = e.auth.SetPassword(as(id, "USER", "user", verified.GetTokens().GetSessionId()), &authv1.SetPasswordRequest{Password: "my-new-password"})
	require.NoError(t, err)

	login, err := e.auth.Login(bg, &authv1.LoginRequest{Type: authv1.AccountType_ACCOUNT_TYPE_USER, Email: "buyer@example.com", Password: "my-new-password"})
	require.NoError(t, err)
	assert.Equal(t, id, login.GetAccount().GetId())
}

func TestLogoutAndGetAccountUseThePrincipal(t *testing.T) {
	e := start(t)
	a, _ := e.auth.SignUpShop(bg, &authv1.SignUpShopRequest{Name: "A", Email: "a@acme.com", Password: "correct-horse"})
	b, _ := e.auth.SignUpShop(bg, &authv1.SignUpShopRequest{Name: "B", Email: "b@acme.com", Password: "correct-horse"})
	idA, sidA := a.GetAccount().GetId(), a.GetTokens().GetSessionId()
	idB := b.GetAccount().GetId()

	_, err := e.auth.GetAccount(bg, &authv1.GetAccountRequest{AccountId: idA})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))

	got, err := e.auth.GetAccount(as(idA, "SHOP", "shop", sidA), &authv1.GetAccountRequest{})
	require.NoError(t, err)
	assert.Equal(t, idA, got.GetId(), "an empty id means the caller's own account")

	_, err = e.auth.GetAccount(as(idA, "SHOP", "shop", sidA), &authv1.GetAccountRequest{AccountId: idB})
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err), "shops cannot read other accounts")
	other, err := e.auth.GetAccount(as("admin-id", "USER", "user,admin", "sid-admin"), &authv1.GetAccountRequest{AccountId: idB})
	require.NoError(t, err)
	assert.Equal(t, idB, other.GetId(), "admins can")
	_, err = e.auth.GetAccount(as(idA, "SHOP", "shop", sidA), &authv1.GetAccountRequest{AccountId: idA + "-missing"})
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))

	_, err = e.auth.Logout(bg, &authv1.LogoutRequest{})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = e.auth.Logout(as(idA, "SHOP", "shop", sidA), &authv1.LogoutRequest{}) // session id falls back to the token's sid
	require.NoError(t, err)
	_, err = e.auth.Refresh(bg, &authv1.RefreshRequest{RefreshToken: a.GetTokens().GetRefreshToken()})
	assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(err))
	_, err = e.auth.Logout(as(idB, "SHOP", "shop", "x"), &authv1.LogoutRequest{SessionId: b.GetTokens().GetSessionId()})
	require.NoError(t, err, "explicit session id of the caller's own session")
}

func TestValidateApiKeyOverGRPC(t *testing.T) {
	e := start(t)
	require.NoError(t, e.w.ApiKeys().Save(bg, &domain.ApiKey{ID: "k", Name: "web", KeyHash: domain.HashToken("good"), Permissions: []string{"0000"}, Active: true}))
	ok, err := e.auth.ValidateApiKey(bg, &authv1.ValidateApiKeyRequest{Key: "good"})
	require.NoError(t, err)
	assert.True(t, ok.GetValid())
	assert.Equal(t, []string{"0000"}, ok.GetPermissions())
	bad, err := e.auth.ValidateApiKey(bg, &authv1.ValidateApiKeyRequest{Key: "bad"})
	require.NoError(t, err)
	assert.False(t, bad.GetValid())
	assert.Empty(t, bad.GetPermissions())
}

func TestRbacOverGRPC(t *testing.T) {
	e := start(t)
	res, err := e.rbac.CreateResource(bg, &authv1.CreateResourceRequest{Name: "Profile", Description: "user profile"})
	require.NoError(t, err)
	assert.Equal(t, "profile", res.GetName())
	_, err = e.rbac.CreateResource(bg, &authv1.CreateResourceRequest{Name: "profile"})
	assert.Equal(t, domain.CodeResourceExists, errs.CodeOf(err))

	role, err := e.rbac.CreateRole(bg, &authv1.CreateRoleRequest{Name: "shop", Description: "sellers",
		Grants: []*authv1.Grant{{Resource: "profile", Actions: []string{"readOwn", "updateOwn"}}}})
	require.NoError(t, err)
	assert.Equal(t, "shop", role.GetName())
	assert.Equal(t, "active", role.GetStatus())
	require.Len(t, role.GetGrants(), 1)
	assert.Equal(t, "*", role.GetGrants()[0].GetAttributes())

	_, err = e.rbac.CreateRole(bg, &authv1.CreateRoleRequest{Name: "ghost", Grants: []*authv1.Grant{{Resource: "nope", Actions: []string{"readAny"}}}})
	assert.Equal(t, domain.CodeResourceNotFound, errs.CodeOf(err))

	roles, err := e.rbac.ListRoles(bg, &emptypb.Empty{})
	require.NoError(t, err)
	assert.Len(t, roles.GetRoles(), 1)
	resources, err := e.rbac.ListResources(bg, &emptypb.Empty{})
	require.NoError(t, err)
	assert.Len(t, resources.GetResources(), 1)
	grants, err := e.rbac.GetGrants(bg, &emptypb.Empty{})
	require.NoError(t, err)
	require.Len(t, grants.GetGrants(), 1)
	g := grants.GetGrants()[0]
	assert.Equal(t, "shop", g.GetRole())
	assert.Equal(t, "profile", g.GetResource())
	assert.Equal(t, []string{"readOwn", "updateOwn"}, g.GetActions())
}

func TestUnknownRPCsAreUnimplemented(t *testing.T) {
	// the generated Unimplemented* embeds make adding an RPC to the proto a compile-safe change
	var _ authv1.AuthServiceServer = transport.NewAuthServer(nil)
	var _ authv1.RbacServiceServer = transport.NewRbacServer(nil)
}

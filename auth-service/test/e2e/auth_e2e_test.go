//go:build e2e

// Service-level end-to-end test: talks to a running auth-service container (make -C auth-service/infra/local up,
// then make seed) and checks the whole path — gRPC, PostgreSQL, Redis and the outbox relay publishing to Kafka.
//
//	AUTH_GRPC_ADDR   default localhost:9101
//	KAFKA_BROKERS    default localhost:9094
//	REDIS_ADDR       default localhost:6379
//	E2E_REQUIRED=1   fail instead of skip when the service is not running
package e2e_test

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/jwtx"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type stack struct {
	auth  authv1.AuthServiceClient
	rbac  authv1.RbacServiceClient
	redis *redis.Client
	kafka []string
}

func connect(t *testing.T) *stack {
	t.Helper()
	addr := env("AUTH_GRPC_ADDR", "localhost:9101")
	if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err != nil {
		if os.Getenv("E2E_REQUIRED") == "1" {
			t.Fatalf("auth-service is not reachable at %s: %v", addr, err)
		}
		t.Skipf("auth-service is not running at %s", addr)
	} else {
		_ = c.Close()
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return grpcx.FromStatus(inv(ctx, m, req, reply, cc, o...))
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	rdb := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "localhost:6379")})
	t.Cleanup(func() { _ = rdb.Close() })
	return &stack{auth: authv1.NewAuthServiceClient(conn), rbac: authv1.NewRbacServiceClient(conn), redis: rdb,
		kafka: strings.Split(env("KAFKA_BROKERS", "localhost:9094"), ",")}
}

var bg = context.Background()

func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@e2e.test", prefix, time.Now().UnixNano())
}

func as(id, typ, roles, sid string) context.Context {
	return metadata.AppendToOutgoingContext(bg, grpcx.MDAccountID, id, grpcx.MDAccountType, typ, grpcx.MDRoles, roles, grpcx.MDSessionID, sid)
}

// waitForEvent reads the topic from the beginning until match accepts an envelope.
func (s *stack) waitForEvent(t *testing.T, topic string, match func(event.Envelope) bool) event.Envelope {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(s.kafka...), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	require.NoError(t, err)
	defer cl.Close()
	ctx, cancel := context.WithTimeout(bg, 30*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var found *event.Envelope
		cl.PollFetches(ctx).EachRecord(func(r *kgo.Record) {
			if env, err := event.Parse(r.Value); err == nil && found == nil && match(env) {
				found = &env
			}
		})
		if found != nil {
			return *found
		}
	}
	t.Fatalf("no matching event on %s within the timeout", topic)
	return event.Envelope{}
}

func TestShopSessionLifecycleAgainstTheRealService(t *testing.T) {
	s := connect(t)
	email := uniqueEmail("shop")

	signed, err := s.auth.SignUpShop(bg, &authv1.SignUpShopRequest{Name: "E2E Shop", Email: email, Password: "correct-horse-battery"})
	require.NoError(t, err)
	shopID, sid := signed.GetAccount().GetId(), signed.GetTokens().GetSessionId()

	// the access token verifies against the JWKS the service publishes
	jwks, err := s.auth.GetJwks(bg, &emptypb.Empty{})
	require.NoError(t, err)
	set := jwtx.JWKS{}
	for _, k := range jwks.GetKeys() {
		set.Keys = append(set.Keys, jwtx.JWK{Kty: k.GetKty(), Kid: k.GetKid(), Alg: k.GetAlg(), Use: k.GetUse(), N: k.GetN(), E: k.GetE()})
	}
	v := jwtx.NewVerifier("ecom-auth", "ecom", nil, nil)
	require.NoError(t, v.SetKeys(set))
	claims, err := v.Verify(bg, signed.GetTokens().GetAccessToken())
	require.NoError(t, err)
	assert.Equal(t, shopID, claims.AccountID())
	assert.Equal(t, "SHOP", claims.Type)

	// the outbox relay delivered ShopRegistered to Kafka
	ev := s.waitForEvent(t, "auth.shop.registered", func(e event.Envelope) bool { return e.AggregateID == shopID })
	assert.Equal(t, "auth.shop.registered", ev.Type)

	// refresh rotation, then reuse of the old token revokes the session and denylists it in Redis
	second, err := s.auth.Refresh(bg, &authv1.RefreshRequest{RefreshToken: signed.GetTokens().GetRefreshToken()})
	require.NoError(t, err)
	_, err = s.auth.Refresh(bg, &authv1.RefreshRequest{RefreshToken: signed.GetTokens().GetRefreshToken()})
	require.Error(t, err)
	assert.Equal(t, "TOKEN_REUSED", errs.CodeOf(err))
	n, err := s.redis.Exists(bg, "auth:revoked:"+sid).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the revoked session is on the gateway's denylist")
	_, err = s.auth.Refresh(bg, &authv1.RefreshRequest{RefreshToken: second.GetTokens().GetRefreshToken()})
	assert.Equal(t, "TOKEN_INVALID", errs.CodeOf(err), "the session is dead for everyone holding its tokens")

	// a fresh login works and logout denylists that session too
	login, err := s.auth.Login(bg, &authv1.LoginRequest{Type: authv1.AccountType_ACCOUNT_TYPE_SHOP, Email: email, Password: "correct-horse-battery"})
	require.NoError(t, err)
	_, err = s.auth.Logout(as(shopID, "SHOP", "shop", login.GetTokens().GetSessionId()), &authv1.LogoutRequest{})
	require.NoError(t, err)
	n, _ = s.redis.Exists(bg, "auth:revoked:"+login.GetTokens().GetSessionId()).Result()
	assert.Equal(t, int64(1), n)
}

func TestBuyerRegistrationThroughTheMailedLink(t *testing.T) {
	s := connect(t)
	email := uniqueEmail("buyer")

	_, err := s.auth.RequestEmailVerification(bg, &authv1.RequestEmailVerificationRequest{Email: email})
	require.NoError(t, err)

	// notification-service would mail this event; here we read the link from Kafka
	ev := s.waitForEvent(t, "auth.email-verification.requested", func(e event.Envelope) bool {
		var p struct{ Email string }
		return e.Decode(&p) == nil && p.Email == email
	})
	var payload struct{ Link string }
	require.NoError(t, ev.Decode(&payload))
	u, err := url.Parse(payload.Link)
	require.NoError(t, err)
	token := u.Query().Get("token")
	require.NotEmpty(t, token)

	verified, err := s.auth.VerifyEmail(bg, &authv1.VerifyEmailRequest{Token: token})
	require.NoError(t, err)
	assert.False(t, verified.GetAccount().GetHasPassword())
	_, err = s.auth.VerifyEmail(bg, &authv1.VerifyEmailRequest{Token: token})
	assert.Equal(t, "TOKEN_INVALID", errs.CodeOf(err), "the link works once")

	id := verified.GetAccount().GetId()
	_, err = s.auth.SetPassword(as(id, "USER", "user", verified.GetTokens().GetSessionId()), &authv1.SetPasswordRequest{Password: "buyer-password-1"})
	require.NoError(t, err)
	login, err := s.auth.Login(bg, &authv1.LoginRequest{Type: authv1.AccountType_ACCOUNT_TYPE_USER, Email: email, Password: "buyer-password-1"})
	require.NoError(t, err)
	assert.Equal(t, id, login.GetAccount().GetId())

	s.waitForEvent(t, "auth.user.registered", func(e event.Envelope) bool { return e.AggregateID == id })
}

func TestSeededAdminApiKeyAndGrants(t *testing.T) {
	s := connect(t)

	key, err := s.auth.ValidateApiKey(bg, &authv1.ValidateApiKeyRequest{Key: env("SEED_API_KEY", "dev-api-key-change-me")})
	require.NoError(t, err)
	assert.True(t, key.GetValid(), "run `make -C auth-service/infra/local seed` first")
	assert.Equal(t, []string{"0000"}, key.GetPermissions())
	bad, err := s.auth.ValidateApiKey(bg, &authv1.ValidateApiKeyRequest{Key: "not-a-key"})
	require.NoError(t, err)
	assert.False(t, bad.GetValid())

	admin, err := s.auth.Login(bg, &authv1.LoginRequest{Type: authv1.AccountType_ACCOUNT_TYPE_USER,
		Email: env("SEED_ADMIN_EMAIL", "admin@ecom.local"), Password: env("SEED_ADMIN_PASSWORD", "admin-change-me-123")})
	require.NoError(t, err)
	assert.Contains(t, admin.GetAccount().GetRoles(), "admin")

	grants, err := s.rbac.GetGrants(bg, &emptypb.Empty{})
	require.NoError(t, err)
	found := map[string]bool{}
	for _, g := range grants.GetGrants() {
		found[g.GetRole()+":"+g.GetResource()] = true
	}
	for _, want := range []string{"user:profile", "shop:profile", "admin:profile", "admin:rbac", "admin:template"} {
		assert.True(t, found[want], want)
	}

	// wrong password and unknown account are indistinguishable
	_, err = s.auth.Login(bg, &authv1.LoginRequest{Type: authv1.AccountType_ACCOUNT_TYPE_USER, Email: "admin@ecom.local", Password: "wrong-wrong-wrong"})
	assert.Equal(t, "INVALID_CREDENTIALS", errs.CodeOf(err))
	_, err = s.auth.Login(bg, &authv1.LoginRequest{Type: authv1.AccountType_ACCOUNT_TYPE_USER, Email: "nobody@e2e.test", Password: "wrong-wrong-wrong"})
	assert.Equal(t, "INVALID_CREDENTIALS", errs.CodeOf(err))
}

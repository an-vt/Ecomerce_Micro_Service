// Package repotest is the repository contract suite: the same behavioural tests run against the in-memory fakes
// (unit tests) and against PostgreSQL (integration tests), so a fake can never promise something the real
// repository does not do.
package repotest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/principal"
)

// Repos are the repositories under test.
type Repos struct {
	Accounts domain.AccountRepository
	Sessions domain.SessionRepository
	Otps     domain.OtpRepository
	ApiKeys  domain.ApiKeyRepository
	Rbac     domain.RbacRepository
}

// New returns a fresh, empty set of repositories for one test.
type New func(t *testing.T) Repos

// now is truncated to microseconds, the precision PostgreSQL stores.
var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

var ctx = context.Background()

func email(t *testing.T, s string) domain.Email {
	t.Helper()
	e, err := domain.ParseEmail(s)
	require.NoError(t, err)
	return e
}

func shop(t *testing.T, id, mail string) *domain.Account {
	t.Helper()
	a, err := domain.NewShop(id, "Shop "+id, email(t, mail), "hash-"+id, now)
	require.NoError(t, err)
	return a
}

// Run executes the whole suite.
func Run(t *testing.T, newRepos New) {
	t.Run("Accounts", func(t *testing.T) { accounts(t, newRepos(t)) })
	t.Run("Sessions", func(t *testing.T) { sessions(t, newRepos(t)) })
	t.Run("Otps", func(t *testing.T) { otps(t, newRepos(t)) })
	t.Run("ApiKeys", func(t *testing.T) { apiKeys(t, newRepos(t)) })
	t.Run("Rbac", func(t *testing.T) { rbac(t, newRepos(t)) })
}

func accounts(t *testing.T, r Repos) {
	a := shop(t, "00000000-0000-4000-8000-000000000001", "shop@example.com")
	require.NoError(t, r.Accounts.Create(ctx, a))

	got, err := r.Accounts.FindByID(ctx, a.ID())
	require.NoError(t, err)
	assert.Equal(t, a.Snapshot(), got.Snapshot(), "round trip keeps every field")

	byEmail, err := r.Accounts.FindByEmail(ctx, principal.TypeShop, email(t, "shop@example.com"))
	require.NoError(t, err)
	assert.Equal(t, a.ID(), byEmail.ID())

	dup := shop(t, "00000000-0000-4000-8000-000000000002", "shop@example.com")
	err = r.Accounts.Create(ctx, dup)
	require.Error(t, err)
	assert.Equal(t, domain.CodeEmailTaken, errs.CodeOf(err), "same type and email is a conflict")
	assert.Equal(t, errs.KindAlreadyExists, errs.KindOf(err))

	buyer := domain.NewUser("00000000-0000-4000-8000-000000000003", email(t, "shop@example.com"), now)
	require.NoError(t, r.Accounts.Create(ctx, buyer), "the same email may be both a shop and a buyer")
	u, err := r.Accounts.FindByEmail(ctx, principal.TypeUser, email(t, "shop@example.com"))
	require.NoError(t, err)
	assert.Equal(t, buyer.ID(), u.ID())

	_, err = r.Accounts.FindByID(ctx, "00000000-0000-4000-8000-0000000000ff")
	assert.Equal(t, domain.CodeAccountNotFound, errs.CodeOf(err))
	assert.Equal(t, errs.KindNotFound, errs.KindOf(err))
	_, err = r.Accounts.FindByEmail(ctx, principal.TypeShop, email(t, "nobody@example.com"))
	assert.Equal(t, domain.CodeAccountNotFound, errs.CodeOf(err))

	require.NoError(t, buyer.SetPasswordHash("new-hash", now.Add(time.Hour)))
	require.NoError(t, r.Accounts.Update(ctx, buyer))
	again, err := r.Accounts.FindByID(ctx, buyer.ID())
	require.NoError(t, err)
	assert.Equal(t, "new-hash", again.PasswordHash())
	assert.True(t, again.HasPassword())

	ghost := shop(t, "00000000-0000-4000-8000-0000000000fe", "ghost@example.com")
	assert.Equal(t, domain.CodeAccountNotFound, errs.CodeOf(r.Accounts.Update(ctx, ghost)))
}

func sessions(t *testing.T, r Repos) {
	acc := shop(t, "00000000-0000-4000-8000-000000000011", "s@example.com")
	require.NoError(t, r.Accounts.Create(ctx, acc))

	s := domain.NewSession("00000000-0000-4000-8000-0000000000a1", acc.ID(), "h1", now, time.Hour)
	require.NoError(t, r.Sessions.Create(ctx, s))

	byID, err := r.Sessions.FindByID(ctx, s.ID())
	require.NoError(t, err)
	assert.Equal(t, s.Snapshot(), byID.Snapshot())
	byHash, err := r.Sessions.FindByRefreshHash(ctx, "h1")
	require.NoError(t, err)
	assert.Equal(t, s.ID(), byHash.ID())

	_, err = r.Sessions.FindByRefreshHash(ctx, "unknown")
	assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(err))
	_, err = r.Sessions.FindByID(ctx, "00000000-0000-4000-8000-0000000000ee")
	assert.Equal(t, errs.KindNotFound, errs.KindOf(err))

	// rotation: compare-and-swap on the old hash
	rotated := domain.RestoreSession(s.Snapshot())
	old, err := rotated.Rotate("h2", now.Add(time.Minute), time.Hour)
	require.NoError(t, err)
	require.NoError(t, r.Sessions.Rotate(ctx, rotated, old))

	_, err = r.Sessions.FindByRefreshHash(ctx, "h1")
	assert.Error(t, err, "the old token no longer finds the session as current")
	cur, err := r.Sessions.FindByRefreshHash(ctx, "h2")
	require.NoError(t, err)
	assert.Equal(t, []string{"h1"}, cur.UsedHashes())
	usedBy, err := r.Sessions.FindByUsedHash(ctx, "h1")
	require.NoError(t, err)
	assert.Equal(t, s.ID(), usedBy.ID(), "a rotated-away token is traceable to its session")
	_, err = r.Sessions.FindByUsedHash(ctx, "h2")
	assert.Error(t, err, "the current token is not a used one")

	// a second rotation from the stale state loses
	stale := domain.RestoreSession(s.Snapshot())
	oldStale, err := stale.Rotate("h-stale", now.Add(2*time.Minute), time.Hour)
	require.NoError(t, err)
	err = r.Sessions.Rotate(ctx, stale, oldStale)
	require.Error(t, err)
	assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(err))

	// revocation persists, and a revoked session cannot be rotated
	cur.Revoke(domain.RevokeReuseDetected, now.Add(3*time.Minute))
	require.NoError(t, r.Sessions.Revoke(ctx, cur))
	after, err := r.Sessions.FindByID(ctx, s.ID())
	require.NoError(t, err)
	assert.True(t, after.Revoked())
	assert.Equal(t, domain.RevokeReuseDetected, after.RevokeReason())

	attempt := domain.RestoreSession(domain.SessionSnapshot{ID: s.ID(), AccountID: acc.ID(), RefreshHash: "h3", UsedHashes: []string{"h1", "h2"}, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now})
	assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(r.Sessions.Rotate(ctx, attempt, "h2")), "revoked sessions never rotate")
}

func otps(t *testing.T, r Repos) {
	o := domain.NewOtp("00000000-0000-4000-8000-0000000000b1", email(t, "buyer@example.com"), "otp-hash", now, 5*time.Minute)
	require.NoError(t, r.Otps.Create(ctx, o))

	got, err := r.Otps.FindByTokenHash(ctx, "otp-hash")
	require.NoError(t, err)
	assert.Equal(t, o.Snapshot(), got.Snapshot())
	_, err = r.Otps.FindByTokenHash(ctx, "nope")
	assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(err))

	require.NoError(t, got.Consume(now.Add(time.Minute)))
	require.NoError(t, r.Otps.MarkUsed(ctx, got))
	again := domain.RestoreOtp(o.Snapshot()) // a concurrent request that read the token before it was used
	require.NoError(t, again.Consume(now.Add(time.Minute)))
	assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(r.Otps.MarkUsed(ctx, again)), "only one request can consume a token")
}

func apiKeys(t *testing.T, r Repos) {
	k, err := r.ApiKeys.FindByHash(ctx, "missing")
	require.NoError(t, err)
	assert.Nil(t, k, "unknown key: nil, no error")

	require.NoError(t, r.ApiKeys.Save(ctx, &domain.ApiKey{ID: "00000000-0000-4000-8000-0000000000c1", Name: "dev", KeyHash: "kh", Permissions: []string{"0000"}, Active: true}))
	got, err := r.ApiKeys.FindByHash(ctx, "kh")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Allows("0000"))

	require.NoError(t, r.ApiKeys.Save(ctx, &domain.ApiKey{ID: "00000000-0000-4000-8000-0000000000c2", Name: "dev", KeyHash: "kh", Permissions: []string{"1111"}, Active: false}))
	got, err = r.ApiKeys.FindByHash(ctx, "kh")
	require.NoError(t, err)
	assert.Equal(t, []string{"1111"}, got.Permissions, "Save is an upsert by key hash")
	assert.False(t, got.Active)
}

func rbac(t *testing.T, r Repos) {
	for _, n := range []string{"profile", "product", "rbac"} {
		res, err := domain.NewResource("00000000-0000-4000-8000-00000000d0"+map[string]string{"profile": "01", "product": "02", "rbac": "03"}[n], n, "", n+" resource")
		require.NoError(t, err)
		require.NoError(t, r.Rbac.CreateResource(ctx, res))
	}
	dup, _ := domain.NewResource("00000000-0000-4000-8000-00000000d0ff", "profile", "", "")
	err := r.Rbac.CreateResource(ctx, dup)
	assert.Equal(t, domain.CodeResourceExists, errs.CodeOf(err))

	resources, err := r.Rbac.ListResources(ctx)
	require.NoError(t, err)
	require.Len(t, resources, 3)
	assert.Equal(t, []string{"product", "profile", "rbac"}, []string{resources[0].Name, resources[1].Name, resources[2].Name}, "sorted by name")

	missing, err := r.Rbac.MissingResources(ctx, []string{"profile", "ghost", "rbac", "other"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ghost", "other"}, missing)

	shopRole, err := domain.NewRole("00000000-0000-4000-8000-00000000e001", "shop", "", "", "sellers", []domain.Grant{
		{Resource: "profile", Actions: []string{"readOwn", "updateOwn"}},
		{Resource: "product", Actions: []string{"createOwn"}, Attributes: "name,price"},
	})
	require.NoError(t, err)
	require.NoError(t, r.Rbac.CreateRole(ctx, shopRole))
	adminRole, _ := domain.NewRole("00000000-0000-4000-8000-00000000e002", "admin", "", "", "", []domain.Grant{{Resource: "rbac", Actions: []string{"createAny", "readAny"}}})
	require.NoError(t, r.Rbac.CreateRole(ctx, adminRole))
	blocked, _ := domain.NewRole("00000000-0000-4000-8000-00000000e003", "ghost", "", domain.RoleBlocked, "", []domain.Grant{{Resource: "rbac", Actions: []string{"readAny"}}})
	require.NoError(t, r.Rbac.CreateRole(ctx, blocked))

	dupRole, _ := domain.NewRole("00000000-0000-4000-8000-00000000e0ff", "shop", "", "", "", nil)
	assert.Equal(t, domain.CodeRoleExists, errs.CodeOf(r.Rbac.CreateRole(ctx, dupRole)))

	roles, err := r.Rbac.ListRoles(ctx)
	require.NoError(t, err)
	require.Len(t, roles, 3)
	assert.Equal(t, "admin", roles[0].Name, "sorted by name")
	assert.Equal(t, "shop", roles[2].Name)
	assert.Equal(t, shopRole.Grants, roles[2].Grants, "grants round trip, in resource order")

	grants, err := r.Rbac.ActiveGrants(ctx)
	require.NoError(t, err)
	require.Len(t, grants, 3, "blocked roles contribute nothing")
	assert.Equal(t, domain.RoleGrant{Role: "admin", Resource: "rbac", Actions: []string{"createAny", "readAny"}, Attributes: "*"}, grants[0])
	assert.Equal(t, "product", grants[1].Resource)
	assert.Equal(t, "name,price", grants[1].Attributes)

	// upsert replaces grants and keeps the id
	changed, _ := domain.NewRole("00000000-0000-4000-8000-00000000e999", "shop", "", "", "sellers v2", []domain.Grant{{Resource: "profile", Actions: []string{"readOwn"}}})
	require.NoError(t, r.Rbac.UpsertRole(ctx, changed))
	roles, err = r.Rbac.ListRoles(ctx)
	require.NoError(t, err)
	require.Len(t, roles, 3)
	assert.Equal(t, "sellers v2", roles[2].Description)
	assert.Equal(t, []domain.Grant{{Resource: "profile", Actions: []string{"readOwn"}, Attributes: "*"}}, roles[2].Grants)
	assert.Equal(t, shopRole.ID, roles[2].ID, "an upsert keeps the existing id")

	res, _ := domain.NewResource("00000000-0000-4000-8000-00000000d999", "profile", "", "updated")
	require.NoError(t, r.Rbac.UpsertResource(ctx, res))
	resources, _ = r.Rbac.ListResources(ctx)
	assert.Equal(t, "updated", resources[1].Description)
	fresh, _ := domain.NewResource("00000000-0000-4000-8000-00000000d998", "template", "", "")
	require.NoError(t, r.Rbac.UpsertResource(ctx, fresh), "upsert also inserts")
}

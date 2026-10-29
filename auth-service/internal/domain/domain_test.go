package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/principal"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestParseEmail(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"a@b.co", "a@b.co", true},
		{"  Shop.Owner+tag@Example.COM ", "shop.owner+tag@example.com", true},
		{"", "", false},
		{"plain", "", false},
		{"a@b", "", false},
		{"@b.co", "", false},
		{"Name <a@b.co>", "", false},
		{"a@b.co, c@d.co", "", false},
		{strings.Repeat("a", 320) + "@b.co", "", false},
	}
	for _, tt := range tests {
		got, err := domain.ParseEmail(tt.in)
		if !tt.ok {
			require.Error(t, err, tt.in)
			assert.Equal(t, domain.CodeInvalidEmail, errs.CodeOf(err))
			continue
		}
		require.NoError(t, err, tt.in)
		assert.Equal(t, tt.want, got.String())
	}
	e, _ := domain.ParseEmail("alice@example.com")
	assert.Equal(t, "alice", e.LocalPart())
}

func TestValidatePassword(t *testing.T) {
	assert.NoError(t, domain.ValidatePassword("12345678"))
	assert.NoError(t, domain.ValidatePassword(strings.Repeat("x", 72)))
	for _, bad := range []string{"", "short", strings.Repeat("x", 73)} {
		err := domain.ValidatePassword(bad)
		require.Error(t, err)
		assert.Equal(t, domain.CodeWeakPassword, errs.CodeOf(err))
	}
}

func TestNewShop(t *testing.T) {
	email, _ := domain.ParseEmail("shop@example.com")
	a, err := domain.NewShop("id1", "  My Shop ", email, "hash", now)
	require.NoError(t, err)
	assert.Equal(t, "My Shop", a.Name())
	assert.Equal(t, principal.TypeShop, a.Type())
	assert.Equal(t, domain.StatusActive, a.Status())
	assert.Equal(t, []string{"shop"}, a.Roles())
	assert.False(t, a.Verified())
	assert.True(t, a.HasPassword())
	assert.NoError(t, a.CheckLogin())

	_, err = domain.NewShop("id", "", email, "hash", now)
	assert.Equal(t, errs.CodeValidationFailed, errs.CodeOf(err))
	_, err = domain.NewShop("id", strings.Repeat("n", 151), email, "hash", now)
	assert.Error(t, err)
	_, err = domain.NewShop("id", "n", email, "", now)
	assert.Error(t, err)
}

func TestNewUserHasNoPasswordUntilSet(t *testing.T) {
	email, _ := domain.ParseEmail("buyer@example.com")
	u := domain.NewUser("u1", email, now)
	assert.Equal(t, principal.TypeUser, u.Type())
	assert.True(t, u.Verified())
	assert.Equal(t, "buyer", u.Name())
	assert.Equal(t, []string{"user"}, u.Roles())
	assert.False(t, u.HasPassword())

	err := u.CheckLogin()
	assert.Equal(t, domain.CodePasswordNotSet, errs.CodeOf(err))
	assert.Equal(t, errs.KindFailedPrecondition, errs.KindOf(err))

	require.NoError(t, u.SetPasswordHash("hash", now.Add(time.Minute)))
	assert.NoError(t, u.CheckLogin())
	assert.Equal(t, "hash", u.PasswordHash())
	assert.Error(t, u.SetPasswordHash("", now))
}

func TestCheckLoginRejectsNonActiveAccounts(t *testing.T) {
	email, _ := domain.ParseEmail("a@b.co")
	for _, st := range []domain.Status{domain.StatusPending, domain.StatusInactive, domain.StatusBlocked} {
		a := domain.RestoreAccount(domain.AccountSnapshot{ID: "x", Type: principal.TypeShop, Email: email, PasswordHash: "h", Status: st})
		err := a.CheckLogin()
		require.Error(t, err, string(st))
		assert.Equal(t, domain.CodeAccountInactive, errs.CodeOf(err))
		assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
	}
}

func TestAccountSnapshotRoundTripAndIsolation(t *testing.T) {
	email, _ := domain.ParseEmail("a@b.co")
	a, _ := domain.NewShop("id1", "S", email, "h", now)
	snap := a.Snapshot()
	b := domain.RestoreAccount(snap)
	assert.Equal(t, snap, b.Snapshot())

	roles := a.Roles()
	roles[0] = "admin"
	assert.Equal(t, []string{"shop"}, a.Roles(), "callers cannot mutate internal state")
}

func TestSessionRotationRemembersOldTokens(t *testing.T) {
	s := domain.NewSession("s1", "acc", "h1", now, time.Hour)
	assert.True(t, s.IsActive(now))
	assert.Equal(t, "h1", s.RefreshHash())
	assert.False(t, s.HasUsed("h1"))

	old, err := s.Rotate("h2", now.Add(10*time.Minute), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "h1", old, "the replaced hash is the compare-and-swap value")
	assert.Equal(t, "h2", s.RefreshHash())
	assert.True(t, s.HasUsed("h1"))
	assert.False(t, s.HasUsed("h2"))
	assert.Equal(t, now.Add(70*time.Minute), s.ExpiresAt(), "rotation slides the expiry")

	_, err = s.Rotate("h3", now.Add(20*time.Minute), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, []string{"h1", "h2"}, s.UsedHashes())
}

func TestSessionExpiryAndRevocation(t *testing.T) {
	s := domain.NewSession("s1", "acc", "h1", now, time.Hour)
	assert.False(t, s.IsActive(now.Add(time.Hour)), "expires exactly at the deadline")
	_, err := s.Rotate("h2", now.Add(2*time.Hour), time.Hour)
	assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(err))
	assert.Equal(t, "h1", s.RefreshHash(), "a failed rotation changes nothing")

	s.Revoke(domain.RevokeLogout, now)
	assert.True(t, s.Revoked())
	assert.False(t, s.IsActive(now))
	s.Revoke(domain.RevokeReuseDetected, now.Add(time.Minute))
	assert.Equal(t, domain.RevokeLogout, s.RevokeReason(), "the first reason sticks")
	_, err = s.Rotate("h9", now, time.Hour)
	assert.Error(t, err, "a revoked session cannot rotate")

	again := domain.RestoreSession(s.Snapshot())
	assert.Equal(t, s.Snapshot(), again.Snapshot())
	assert.Equal(t, "acc", again.AccountID())
	assert.Equal(t, "s1", again.ID())
}

func TestOtpIsSingleUseAndExpires(t *testing.T) {
	email, _ := domain.ParseEmail("a@b.co")
	o := domain.NewOtp("o1", email, "hash", now, 5*time.Minute)
	assert.False(t, o.Used())
	assert.Equal(t, now.Add(5*time.Minute), o.ExpiresAt())
	assert.Equal(t, email, o.Email())

	require.NoError(t, o.Consume(now.Add(4*time.Minute)))
	assert.True(t, o.Used())
	err := o.Consume(now.Add(4 * time.Minute))
	assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(err), "second use fails")

	late := domain.NewOtp("o2", email, "h2", now, 5*time.Minute)
	assert.Error(t, late.Consume(now.Add(5*time.Minute)), "expired at the deadline")
	assert.False(t, late.Used())

	assert.Equal(t, o.Snapshot(), domain.RestoreOtp(o.Snapshot()).Snapshot())
	assert.Equal(t, "o1", o.ID())
}

func TestApiKeyAllows(t *testing.T) {
	k := domain.ApiKey{Active: true, Permissions: []string{domain.PermissionAll}}
	assert.True(t, k.Allows("0000"))
	assert.False(t, k.Allows("1111"))
	k.Active = false
	assert.False(t, k.Allows("0000"), "an inactive key allows nothing")
}

func TestNewRole(t *testing.T) {
	r, err := domain.NewRole("r1", " Shop ", "", "", "sellers", []domain.Grant{
		{Resource: "Profile", Actions: []string{"readOwn", "updateOwn"}},
		{Resource: "product", Actions: []string{"createOwn"}, Attributes: "name,price"},
	})
	require.NoError(t, err)
	assert.Equal(t, "shop", r.Name)
	assert.Equal(t, "shop", r.Slug)
	assert.Equal(t, domain.RoleActive, r.Status)
	assert.Equal(t, "product", r.Grants[0].Resource, "grants are kept in resource order")
	assert.Equal(t, "name,price", r.Grants[0].Attributes)
	assert.Equal(t, "profile", r.Grants[1].Resource)
	assert.Equal(t, "*", r.Grants[1].Attributes)

	bad := []struct {
		name, status string
		grants       []domain.Grant
	}{
		{"X", "", nil},
		{"has space", "", nil},
		{"ok", "weird", nil},
		{"ok", "", []domain.Grant{{Resource: "", Actions: []string{"readAny"}}}},
		{"ok", "", []domain.Grant{{Resource: "a", Actions: []string{"readAny"}}, {Resource: "a", Actions: []string{"readOwn"}}}},
		{"ok", "", []domain.Grant{{Resource: "a"}}},
		{"ok", "", []domain.Grant{{Resource: "a", Actions: []string{"destroyAny"}}}},
	}
	for _, b := range bad {
		_, err := domain.NewRole("id", b.name, "", b.status, "", b.grants)
		require.Error(t, err, "%+v", b)
		assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	}
}

func TestNewResource(t *testing.T) {
	r, err := domain.NewResource("id", "Profile", "", "user profile")
	require.NoError(t, err)
	assert.Equal(t, "profile", r.Name)
	assert.Equal(t, "profile", r.Slug)
	_, err = domain.NewResource("id", "Bad Name!", "", "")
	assert.Error(t, err)
}

func TestEventsImplementTheEventContract(t *testing.T) {
	var _ event.Event = domain.ShopRegistered{}
	var _ event.Event = domain.UserRegistered{}
	var _ event.Event = domain.EmailVerificationRequested{}
	assert.Equal(t, "auth.shop.registered", domain.ShopRegistered{ShopID: "s"}.EventType())
	assert.Equal(t, "s", domain.ShopRegistered{ShopID: "s"}.AggregateID())
	assert.Equal(t, "auth.user.registered", domain.UserRegistered{UserID: "u"}.EventType())
	assert.Equal(t, "u", domain.UserRegistered{UserID: "u"}.AggregateID())
	assert.Equal(t, "auth.email-verification.requested", domain.EmailVerificationRequested{OtpID: "o"}.EventType())
	assert.Equal(t, "o", domain.EmailVerificationRequested{OtpID: "o"}.AggregateID())
}

func TestErrorConstructors(t *testing.T) {
	assert.Equal(t, errs.KindAlreadyExists, errs.KindOf(domain.ErrEmailTaken()))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(domain.ErrInvalidCredentials()))
	assert.Equal(t, errs.KindNotFound, errs.KindOf(domain.ErrAccountNotFound()))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(domain.ErrTokenInvalid()))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(domain.ErrTokenReused()))
	assert.Equal(t, domain.CodeTokenReused, errs.CodeOf(domain.ErrTokenReused()))
}

package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/auth-service/internal/fake"
	"github.com/an-vt/ecom/auth-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/jwtx"
	"github.com/an-vt/ecom/platform-go/principal"
)

var ctx = context.Background()

type fx struct {
	w        *fake.World
	clk      *clock.Fixed
	hasher   *fake.Hasher
	secrets  *fake.Secrets
	set      *usecase.Set
	verifier *jwtx.Verifier
}

func newFx(t *testing.T) *fx {
	t.Helper()
	clk := clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	w := fake.NewWorld()
	signer := fake.NewSigner(15*time.Minute, clk)
	verifier := jwtx.NewVerifier("ecom-auth", "ecom", clk, nil)
	require.NoError(t, verifier.SetKeys(signer.JWKS()))
	f := &fx{w: w, clk: clk, hasher: &fake.Hasher{}, secrets: &fake.Secrets{}, verifier: verifier}
	f.set = usecase.NewSet(&usecase.Deps{
		Accounts: w.Accounts(), Sessions: w.Sessions(), Otps: w.Otps(), ApiKeys: w.ApiKeys(), Rbac: w.Rbac(),
		Hasher: f.hasher, Signer: signer, Secrets: f.secrets, Denylist: w, Tx: w, Outbox: w,
		Clock: clk, IDs: &idgen.Sequence{Prefix: "id-"},
		Settings: usecase.Settings{RefreshTTL: 7 * 24 * time.Hour, OTPTTL: 5 * time.Minute, VerifyLinkBase: "http://localhost:8088/v1/api/user/welcome_back"},
	})
	return f
}

func (f *fx) signUp(t *testing.T, email string) usecase.AuthResult {
	t.Helper()
	res, err := f.set.SignUpShop.Execute(ctx, usecase.SignUpShopInput{Name: "Acme", Email: email, Password: "correct-horse"})
	require.NoError(t, err)
	return res
}

func code(err error) string { return errs.CodeOf(err) }

// ---- sign up -------------------------------------------------------------------------------------------------

func TestSignUpShopCreatesAccountSessionAndEvent(t *testing.T) {
	f := newFx(t)
	res := f.signUp(t, " Owner@Acme.com ")

	assert.Equal(t, "owner@acme.com", res.Account.Email)
	assert.Equal(t, "SHOP", res.Account.Type)
	assert.Equal(t, []string{"shop"}, res.Account.Roles)
	assert.True(t, res.Account.HasPassword)
	assert.Equal(t, 15*time.Minute, res.Tokens.ExpiresIn)

	claims, err := f.verifier.Verify(ctx, res.Tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, res.Account.ID, claims.AccountID())
	assert.Equal(t, "SHOP", claims.Type)
	assert.Equal(t, res.Tokens.SessionID, claims.SessionID)

	assert.Equal(t, 1, f.w.AccountCount())
	assert.Equal(t, 1, f.w.SessionCount())
	assert.Equal(t, []string{"auth.shop.registered"}, f.w.OutboxTypes())
	env, err := event.Parse(f.w.Outbox[0].Payload)
	require.NoError(t, err)
	var ev domain.ShopRegistered
	require.NoError(t, env.Decode(&ev))
	assert.Equal(t, domain.ShopRegistered{ShopID: res.Account.ID, Name: "Acme", Email: "owner@acme.com"}, ev)
	assert.Equal(t, "auth.shop.registered", f.w.Outbox[0].Topic)
	assert.Equal(t, res.Account.ID, f.w.Outbox[0].Key)
}

func TestSignUpShopRejectionsLeaveNothingBehind(t *testing.T) {
	f := newFx(t)
	f.signUp(t, "a@acme.com")

	_, err := f.set.SignUpShop.Execute(ctx, usecase.SignUpShopInput{Name: "Other", Email: "A@acme.com", Password: "correct-horse"})
	assert.Equal(t, domain.CodeEmailTaken, code(err))
	assert.Equal(t, 1, f.w.AccountCount())
	assert.Equal(t, 1, f.w.SessionCount())
	assert.Len(t, f.w.Outbox, 1)

	for name, in := range map[string]usecase.SignUpShopInput{
		"bad email":     {Name: "n", Email: "nope", Password: "correct-horse"},
		"weak password": {Name: "n", Email: "b@acme.com", Password: "short"},
		"no name":       {Name: " ", Email: "b@acme.com", Password: "correct-horse"},
	} {
		_, err := f.set.SignUpShop.Execute(ctx, in)
		require.Error(t, err, name)
		assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err), name)
	}
	assert.Equal(t, 1, f.w.AccountCount())
}

func TestSignUpShopIsAtomic(t *testing.T) {
	f := newFx(t)
	f.w.FailOutbox = errors.New("outbox unavailable")
	_, err := f.set.SignUpShop.Execute(ctx, usecase.SignUpShopInput{Name: "n", Email: "a@acme.com", Password: "correct-horse"})
	require.Error(t, err)
	assert.Equal(t, 0, f.w.AccountCount(), "no account without its event")
	assert.Equal(t, 0, f.w.SessionCount())

	f.w.FailOutbox = nil
	f.secrets.Fail = errors.New("rng down")
	_, err = f.set.SignUpShop.Execute(ctx, usecase.SignUpShopInput{Name: "n", Email: "a@acme.com", Password: "correct-horse"})
	require.Error(t, err)
	assert.Equal(t, 0, f.w.AccountCount(), "no account without a session")
	assert.Empty(t, f.w.Outbox)

	f.secrets.Fail = nil
	f.hasher.FailHash = errors.New("bcrypt failed")
	_, err = f.set.SignUpShop.Execute(ctx, usecase.SignUpShopInput{Name: "n", Email: "a@acme.com", Password: "correct-horse"})
	assert.Error(t, err)
}

// ---- login ---------------------------------------------------------------------------------------------------

func TestLoginSuccess(t *testing.T) {
	f := newFx(t)
	signed := f.signUp(t, "a@acme.com")
	res, err := f.set.Login.Execute(ctx, usecase.LoginInput{Type: principal.TypeShop, Email: "A@Acme.com", Password: "correct-horse"})
	require.NoError(t, err)
	assert.Equal(t, signed.Account.ID, res.Account.ID)
	assert.NotEqual(t, signed.Tokens.SessionID, res.Tokens.SessionID, "each login is its own session: multiple devices")
	assert.Equal(t, 2, f.w.SessionCount())
	_, err = f.verifier.Verify(ctx, res.Tokens.AccessToken)
	assert.NoError(t, err)
}

func TestLoginFailuresAreIndistinguishableAndCostTheSame(t *testing.T) {
	f := newFx(t)
	f.signUp(t, "a@acme.com")
	buyer := domain.NewUser("u-1", mustEmail(t, "buyer@acme.com"), f.clk.Now()) // verified, no password yet
	require.NoError(t, f.w.Accounts().Create(ctx, buyer))

	cases := map[string]usecase.LoginInput{
		"wrong password":         {Type: principal.TypeShop, Email: "a@acme.com", Password: "wrong-password"},
		"unknown email":          {Type: principal.TypeShop, Email: "ghost@acme.com", Password: "correct-horse"},
		"malformed email":        {Type: principal.TypeShop, Email: "not-an-email", Password: "correct-horse"},
		"right email wrong type": {Type: principal.TypeUser, Email: "a@acme.com", Password: "correct-horse"},
		"buyer without password": {Type: principal.TypeUser, Email: "buyer@acme.com", Password: "anything-here"},
	}
	for name, in := range cases {
		before := f.hasher.Compares
		_, err := f.set.Login.Execute(ctx, in)
		require.Error(t, err, name)
		assert.Equal(t, domain.CodeInvalidCredentials, code(err), name)
		assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err), name)
		assert.Equal(t, 1, f.hasher.Compares-before, "%s: exactly one password comparison, so timing does not reveal which case it was", name)
	}
	assert.Equal(t, 1, f.w.SessionCount(), "no session for failed logins")
}

func TestLoginRejectsInactiveAccountsAfterTheCorrectPassword(t *testing.T) {
	f := newFx(t)
	signed := f.signUp(t, "a@acme.com")
	acc, _ := f.w.Accounts().FindByID(ctx, signed.Account.ID)
	snap := acc.Snapshot()
	snap.Status = domain.StatusBlocked
	require.NoError(t, f.w.Accounts().Update(ctx, domain.RestoreAccount(snap)))

	_, err := f.set.Login.Execute(ctx, usecase.LoginInput{Type: principal.TypeShop, Email: "a@acme.com", Password: "correct-horse"})
	assert.Equal(t, domain.CodeAccountInactive, code(err))
	_, err = f.set.Login.Execute(ctx, usecase.LoginInput{Type: principal.TypeShop, Email: "a@acme.com", Password: "wrong-password"})
	assert.Equal(t, domain.CodeInvalidCredentials, code(err), "a wrong password never reveals the account state")
}

func TestLoginValidatesTypeAndFailsAtomically(t *testing.T) {
	f := newFx(t)
	f.signUp(t, "a@acme.com")
	_, err := f.set.Login.Execute(ctx, usecase.LoginInput{Type: "ADMIN", Email: "a@acme.com", Password: "correct-horse"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))

	f.secrets.Fail = errors.New("rng down")
	_, err = f.set.Login.Execute(ctx, usecase.LoginInput{Type: principal.TypeShop, Email: "a@acme.com", Password: "correct-horse"})
	assert.Error(t, err)
	assert.Equal(t, 1, f.w.SessionCount())
}

func mustEmail(t *testing.T, s string) domain.Email {
	t.Helper()
	e, err := domain.ParseEmail(s)
	require.NoError(t, err)
	return e
}

// ---- refresh -------------------------------------------------------------------------------------------------

func TestRefreshRotatesTheTokenAndKeepsTheSession(t *testing.T) {
	f := newFx(t)
	first := f.signUp(t, "a@acme.com")
	f.clk.Advance(10 * time.Minute)

	second, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	require.NoError(t, err)
	assert.NotEqual(t, first.Tokens.RefreshToken, second.Tokens.RefreshToken)
	assert.NotEqual(t, first.Tokens.AccessToken, second.Tokens.AccessToken)
	assert.Equal(t, first.Tokens.SessionID, second.Tokens.SessionID)
	assert.Equal(t, 1, f.w.SessionCount())

	third, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: second.Tokens.RefreshToken})
	require.NoError(t, err, "the new token works")
	assert.NotEqual(t, second.Tokens.RefreshToken, third.Tokens.RefreshToken)
	claims, err := f.verifier.Verify(ctx, third.Tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, first.Account.ID, claims.AccountID())
}

func TestReplayingARotatedTokenRevokesTheWholeSession(t *testing.T) {
	f := newFx(t)
	first := f.signUp(t, "a@acme.com")
	second, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	require.NoError(t, err)

	// the attacker (or the legitimate client with a stale copy) presents the old token again
	_, err = f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	require.Error(t, err)
	assert.Equal(t, domain.CodeTokenReused, code(err))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err), "403 at the gateway")

	sess, err := f.w.Sessions().FindByID(ctx, first.Tokens.SessionID)
	require.NoError(t, err)
	assert.True(t, sess.Revoked(), "the revocation outlives the error")
	assert.Equal(t, domain.RevokeReuseDetected, sess.RevokeReason())
	assert.Contains(t, f.w.Denied, first.Tokens.SessionID, "its access tokens stop working immediately")

	_, err = f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: second.Tokens.RefreshToken})
	assert.Equal(t, domain.CodeTokenInvalid, code(err), "...and so does the legitimate holder's newest token: log in again")
}

func TestRefreshRejections(t *testing.T) {
	f := newFx(t)
	first := f.signUp(t, "a@acme.com")

	for name, tok := range map[string]string{"empty": "", "unknown": "never-issued"} {
		_, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: tok})
		assert.Equal(t, domain.CodeTokenInvalid, code(err), name)
	}

	f.clk.Advance(8 * 24 * time.Hour)
	_, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	assert.Equal(t, domain.CodeTokenInvalid, code(err), "expired session")
}

func TestRefreshOfADisabledAccountIsRefused(t *testing.T) {
	f := newFx(t)
	first := f.signUp(t, "a@acme.com")
	acc, _ := f.w.Accounts().FindByID(ctx, first.Account.ID)
	snap := acc.Snapshot()
	snap.Status = domain.StatusInactive
	require.NoError(t, f.w.Accounts().Update(ctx, domain.RestoreAccount(snap)))
	_, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	assert.Equal(t, domain.CodeAccountInactive, code(err))
}

func TestRefreshOfADeletedAccountIsInvalid(t *testing.T) {
	f := newFx(t)
	// a live session whose account no longer exists
	orphan := domain.NewSession("orphan", "no-such-account", domain.HashToken("orphan-secret"), f.clk.Now(), time.Hour)
	require.NoError(t, f.w.Sessions().Create(ctx, orphan))

	_, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: "orphan-secret"})
	assert.Equal(t, domain.CodeTokenInvalid, code(err))
	sess, _ := f.w.Sessions().FindByID(ctx, "orphan")
	assert.Equal(t, domain.HashToken("orphan-secret"), sess.RefreshHash(), "nothing was rotated")
}

func TestRefreshSurfacesSecretGeneratorFailure(t *testing.T) {
	f := newFx(t)
	first := f.signUp(t, "a@acme.com")
	f.secrets.Fail = errors.New("rng down")
	_, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	require.Error(t, err)
	f.secrets.Fail = nil
	_, err = f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	assert.NoError(t, err, "the failed attempt did not consume the token")
}

func TestReuseDetectionReportsInfrastructureFailures(t *testing.T) {
	f := newFx(t)
	first := f.signUp(t, "a@acme.com")
	_, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	require.NoError(t, err)

	f.w.FailDenylist = errors.New("redis down")
	_, err = f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: first.Tokens.RefreshToken})
	require.Error(t, err)
	assert.NotEqual(t, domain.CodeTokenReused, code(err), "the caller learns that the denylist write failed")
	sess, _ := f.w.Sessions().FindByID(ctx, first.Tokens.SessionID)
	assert.True(t, sess.Revoked(), "but the session is revoked in the database regardless")
}

// ---- logout --------------------------------------------------------------------------------------------------

func TestLogoutRevokesAndDenylists(t *testing.T) {
	f := newFx(t)
	res := f.signUp(t, "a@acme.com")
	in := usecase.LogoutInput{AccountID: res.Account.ID, SessionID: res.Tokens.SessionID}
	require.NoError(t, f.set.Logout.Execute(ctx, in))

	sess, _ := f.w.Sessions().FindByID(ctx, res.Tokens.SessionID)
	assert.True(t, sess.Revoked())
	assert.Equal(t, domain.RevokeLogout, sess.RevokeReason())
	assert.Equal(t, 15*time.Minute, f.w.Denied[res.Tokens.SessionID], "denylisted for as long as an access token can live")

	_, err := f.set.Refresh.Execute(ctx, usecase.RefreshInput{RefreshToken: res.Tokens.RefreshToken})
	assert.Equal(t, domain.CodeTokenInvalid, code(err), "a logged-out session cannot refresh")

	require.NoError(t, f.set.Logout.Execute(ctx, in), "logout is idempotent")
	require.NoError(t, f.set.Logout.Execute(ctx, usecase.LogoutInput{AccountID: res.Account.ID, SessionID: "never-existed"}))
}

func TestLogoutCannotEndSomeoneElsesSession(t *testing.T) {
	f := newFx(t)
	a := f.signUp(t, "a@acme.com")
	b := f.signUp(t, "b@acme.com")
	err := f.set.Logout.Execute(ctx, usecase.LogoutInput{AccountID: b.Account.ID, SessionID: a.Tokens.SessionID})
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
	sess, _ := f.w.Sessions().FindByID(ctx, a.Tokens.SessionID)
	assert.False(t, sess.Revoked())

	f.w.FailDenylist = errors.New("redis down")
	assert.Error(t, f.set.Logout.Execute(ctx, usecase.LogoutInput{AccountID: a.Account.ID, SessionID: a.Tokens.SessionID}))
}

// ---- buyer registration --------------------------------------------------------------------------------------

// linkToken extracts the secret from the mailed verification link in the outbox.
func (f *fx) linkToken(t *testing.T, idx int) string {
	t.Helper()
	env, err := event.Parse(f.w.Outbox[idx].Payload)
	require.NoError(t, err)
	var ev domain.EmailVerificationRequested
	require.NoError(t, env.Decode(&ev))
	u, err := url.Parse(ev.Link)
	require.NoError(t, err)
	assert.Equal(t, "/v1/api/user/welcome_back", u.Path)
	return u.Query().Get("token")
}

func TestBuyerRegistrationFlow(t *testing.T) {
	f := newFx(t)
	require.NoError(t, f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "Buyer@Example.com"}))
	assert.Equal(t, []string{"auth.email-verification.requested"}, f.w.OutboxTypes())
	token := f.linkToken(t, 0)
	require.NotEmpty(t, token)

	env, _ := event.Parse(f.w.Outbox[0].Payload)
	var ev domain.EmailVerificationRequested
	require.NoError(t, env.Decode(&ev))
	assert.Equal(t, "buyer@example.com", ev.Email)
	assert.Equal(t, f.clk.Now().Add(5*time.Minute), ev.ExpiresAt)

	res, err := f.set.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: token})
	require.NoError(t, err)
	assert.Equal(t, "USER", res.Account.Type)
	assert.True(t, res.Account.Verified)
	assert.False(t, res.Account.HasPassword, "the buyer chooses a password afterwards")
	assert.Equal(t, []string{"user"}, res.Account.Roles)
	assert.Equal(t, []string{"auth.email-verification.requested", "auth.user.registered"}, f.w.OutboxTypes())

	_, err = f.set.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: token})
	assert.Equal(t, domain.CodeTokenInvalid, code(err), "the link works once")
	assert.Equal(t, 1, f.w.AccountCount())

	require.NoError(t, f.set.SetPassword.Execute(ctx, usecase.SetPasswordInput{AccountID: res.Account.ID, Password: "my-new-password"}))
	login, err := f.set.Login.Execute(ctx, usecase.LoginInput{Type: principal.TypeUser, Email: "buyer@example.com", Password: "my-new-password"})
	require.NoError(t, err)
	assert.Equal(t, res.Account.ID, login.Account.ID)
}

func TestRequestEmailVerificationRejections(t *testing.T) {
	f := newFx(t)
	err := f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "bad"})
	assert.Equal(t, domain.CodeInvalidEmail, code(err))

	require.NoError(t, f.w.Accounts().Create(ctx, domain.NewUser("u-1", mustEmail(t, "taken@example.com"), f.clk.Now())))
	err = f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "taken@example.com"})
	assert.Equal(t, domain.CodeEmailTaken, code(err))

	f.signUp(t, "shopowner@example.com")
	assert.NoError(t, f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "shopowner@example.com"}),
		"a shop account does not block the same address as a buyer")

	f.secrets.Fail = errors.New("rng down")
	assert.Error(t, f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "x@example.com"}))
}

func TestVerifyEmailRejections(t *testing.T) {
	f := newFx(t)
	require.NoError(t, f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "b@example.com"}))
	token := f.linkToken(t, 0)

	for name, tok := range map[string]string{"empty": "", "unknown": "forged"} {
		_, err := f.set.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: tok})
		assert.Equal(t, domain.CodeTokenInvalid, code(err), name)
	}
	f.clk.Advance(6 * time.Minute)
	_, err := f.set.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: token})
	assert.Equal(t, domain.CodeTokenInvalid, code(err), "expired after 5 minutes")
	assert.Equal(t, 0, f.w.AccountCount())
}

func TestVerifyEmailIsAtomicSoALinkSurvivesATransientFailure(t *testing.T) {
	f := newFx(t)
	require.NoError(t, f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "b@example.com"}))
	token := f.linkToken(t, 0)

	f.w.FailOutbox = errors.New("outbox unavailable")
	_, err := f.set.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: token})
	require.Error(t, err)
	assert.Equal(t, 0, f.w.AccountCount())

	f.w.FailOutbox = nil
	res, err := f.set.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: token})
	require.NoError(t, err, "the failed attempt did not burn the link")
	assert.Equal(t, "b@example.com", res.Account.Email)
}

func TestVerifyEmailForAnExistingBuyerJustLogsThemIn(t *testing.T) {
	f := newFx(t)
	require.NoError(t, f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "b@example.com"}))
	require.NoError(t, f.set.RequestEmailVerification.Execute(ctx, usecase.RequestEmailVerificationInput{Email: "b@example.com"})) // two pending links
	first, err := f.set.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: f.linkToken(t, 0)})
	require.NoError(t, err)
	second, err := f.set.VerifyEmail.Execute(ctx, usecase.VerifyEmailInput{Token: f.linkToken(t, 1)})
	require.NoError(t, err)
	assert.Equal(t, first.Account.ID, second.Account.ID)
	assert.Equal(t, 1, f.w.AccountCount())
	count := 0
	for _, ty := range f.w.OutboxTypes() {
		if ty == "auth.user.registered" {
			count++
		}
	}
	assert.Equal(t, 1, count, "UserRegistered is published once")
}

// ---- account -------------------------------------------------------------------------------------------------

func TestSetPasswordAndGetAccount(t *testing.T) {
	f := newFx(t)
	res := f.signUp(t, "a@acme.com")

	require.NoError(t, f.set.SetPassword.Execute(ctx, usecase.SetPasswordInput{AccountID: res.Account.ID, Password: "another-password"}))
	_, err := f.set.Login.Execute(ctx, usecase.LoginInput{Type: principal.TypeShop, Email: "a@acme.com", Password: "correct-horse"})
	assert.Equal(t, domain.CodeInvalidCredentials, code(err), "the old password stops working")
	_, err = f.set.Login.Execute(ctx, usecase.LoginInput{Type: principal.TypeShop, Email: "a@acme.com", Password: "another-password"})
	assert.NoError(t, err)

	assert.Equal(t, domain.CodeWeakPassword, code(f.set.SetPassword.Execute(ctx, usecase.SetPasswordInput{AccountID: res.Account.ID, Password: "short"})))
	assert.Equal(t, domain.CodeAccountNotFound, code(f.set.SetPassword.Execute(ctx, usecase.SetPasswordInput{AccountID: "ghost", Password: "long-enough-pw"})))
	f.hasher.FailHash = errors.New("bcrypt failed")
	assert.Error(t, f.set.SetPassword.Execute(ctx, usecase.SetPasswordInput{AccountID: res.Account.ID, Password: "long-enough-pw"}))

	view, err := f.set.GetAccount.Execute(ctx, res.Account.ID)
	require.NoError(t, err)
	assert.Equal(t, "a@acme.com", view.Email)
	assert.True(t, view.HasPassword)
	_, err = f.set.GetAccount.Execute(ctx, "ghost")
	assert.Equal(t, domain.CodeAccountNotFound, code(err))
}

// ---- api keys / jwks -----------------------------------------------------------------------------------------

func TestValidateApiKey(t *testing.T) {
	f := newFx(t)
	require.NoError(t, f.w.ApiKeys().Save(ctx, &domain.ApiKey{ID: "k1", Name: "web", KeyHash: domain.HashToken("good-key"), Permissions: []string{"0000"}, Active: true}))
	require.NoError(t, f.w.ApiKeys().Save(ctx, &domain.ApiKey{ID: "k2", Name: "old", KeyHash: domain.HashToken("old-key"), Permissions: []string{"0000"}, Active: false}))

	got, err := f.set.ValidateApiKey.Execute(ctx, "good-key")
	require.NoError(t, err)
	assert.Equal(t, usecase.ApiKeyCheck{Valid: true, Permissions: []string{"0000"}}, got)

	for name, key := range map[string]string{"inactive": "old-key", "unknown": "nope", "empty": ""} {
		got, err := f.set.ValidateApiKey.Execute(ctx, key)
		require.NoError(t, err, name)
		assert.Equal(t, usecase.ApiKeyCheck{}, got, "%s: invalid keys reveal no permissions", name)
	}
}

func TestGetJwksPublishesTheVerificationKey(t *testing.T) {
	f := newFx(t)
	set := f.set.GetJwks.Execute()
	require.Len(t, set.Keys, 1)
	assert.Equal(t, "RS256", set.Keys[0].Alg)
}

// ---- rbac ----------------------------------------------------------------------------------------------------

func TestRbacUseCases(t *testing.T) {
	f := newFx(t)
	_, err := f.set.CreateResource.Execute(ctx, usecase.CreateResourceInput{Name: "Profile", Description: "user profile"})
	require.NoError(t, err)
	_, err = f.set.CreateResource.Execute(ctx, usecase.CreateResourceInput{Name: "profile"})
	assert.Equal(t, domain.CodeResourceExists, code(err))
	_, err = f.set.CreateResource.Execute(ctx, usecase.CreateResourceInput{Name: "Bad Name"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))

	_, err = f.set.CreateRole.Execute(ctx, usecase.CreateRoleInput{Name: "shop", Grants: []domain.Grant{{Resource: "ghost", Actions: []string{"readOwn"}}}})
	assert.Equal(t, domain.CodeResourceNotFound, code(err), "grants must refer to existing resources")
	_, err = f.set.CreateRole.Execute(ctx, usecase.CreateRoleInput{Name: "X"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))

	role, err := f.set.CreateRole.Execute(ctx, usecase.CreateRoleInput{Name: "shop", Description: "sellers",
		Grants: []domain.Grant{{Resource: "profile", Actions: []string{"readOwn", "updateOwn"}}}})
	require.NoError(t, err)
	assert.Equal(t, "shop", role.Name)
	_, err = f.set.CreateRole.Execute(ctx, usecase.CreateRoleInput{Name: "shop"})
	assert.Equal(t, domain.CodeRoleExists, code(err))

	roles, err := f.set.ListRoles.Execute(ctx)
	require.NoError(t, err)
	assert.Len(t, roles, 1)
	resources, err := f.set.ListResources.Execute(ctx)
	require.NoError(t, err)
	assert.Len(t, resources, 1)
	grants, err := f.set.GetGrants.Execute(ctx)
	require.NoError(t, err)
	assert.Equal(t, []domain.RoleGrant{{Role: "shop", Resource: "profile", Actions: []string{"readOwn", "updateOwn"}, Attributes: "*"}}, grants)
}

func TestViewsNeverExposeThePasswordHash(t *testing.T) {
	f := newFx(t)
	res := f.signUp(t, "a@acme.com")
	b, err := json.Marshal(res.Account)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "hashed:")
	assert.NotContains(t, string(b), "correct-horse")
}

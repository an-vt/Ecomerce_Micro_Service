package http

import (
	stdhttp "net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/rbac"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
)

const refreshCookie = "refreshToken"

// ---- JSON shapes ----------------------------------------------------------------------------------------------

type accountJSON struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Verified    bool     `json:"verified"`
	HasPassword bool     `json:"hasPassword"`
	Roles       []string `json:"roles"`
}

type tokensJSON struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
	SessionID    string `json:"sessionId"`
}

type authJSON struct {
	Account accountJSON `json:"account"`
	Tokens  tokensJSON  `json:"tokens"`
}

func accountOf(a *authv1.Account) accountJSON {
	typ := "UNSPECIFIED"
	switch a.GetType() {
	case authv1.AccountType_ACCOUNT_TYPE_SHOP:
		typ = "SHOP"
	case authv1.AccountType_ACCOUNT_TYPE_USER:
		typ = "USER"
	}
	roles := a.GetRoles()
	if roles == nil {
		roles = []string{}
	}
	return accountJSON{ID: a.GetId(), Type: typ, Email: a.GetEmail(), Name: a.GetName(), Status: a.GetStatus(),
		Verified: a.GetVerified(), HasPassword: a.GetHasPassword(), Roles: roles}
}

func authOf(r *authv1.AuthResponse) authJSON {
	t := r.GetTokens()
	return authJSON{Account: accountOf(r.GetAccount()),
		Tokens: tokensJSON{AccessToken: t.GetAccessToken(), RefreshToken: t.GetRefreshToken(), ExpiresIn: t.GetExpiresInSeconds(), SessionID: t.GetSessionId()}}
}

// ---- handlers -------------------------------------------------------------------------------------------------

// Auth holds the identity routes.
type Auth struct {
	client       authv1.AuthServiceClient
	cookieSecure bool
	cookieTTL    time.Duration
}

func (h *Auth) setRefreshCookie(c *gin.Context, token string) {
	c.SetSameSite(stdhttp.SameSiteStrictMode)
	c.SetCookie(refreshCookie, token, int(h.cookieTTL.Seconds()), "/v1/api", "", h.cookieSecure, true)
}

func (h *Auth) clearRefreshCookie(c *gin.Context) {
	c.SetSameSite(stdhttp.SameSiteStrictMode)
	c.SetCookie(refreshCookie, "", -1, "/v1/api", "", h.cookieSecure, true)
}

func (h *Auth) respond(c *gin.Context, status int, message string, r *authv1.AuthResponse) {
	h.setRefreshCookie(c, r.GetTokens().GetRefreshToken())
	ok(c, status, message, authOf(r))
}

func (h *Auth) signUpShop(c *gin.Context) {
	var in struct {
		Name     string `json:"name" binding:"required"`
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "name, email and password are required")
		return
	}
	r, err := h.client.SignUpShop(c.Request.Context(), &authv1.SignUpShopRequest{Name: in.Name, Email: in.Email, Password: in.Password})
	if err != nil {
		fail(c, err)
		return
	}
	h.respond(c, stdhttp.StatusCreated, "Registered OK", r)
}

func (h *Auth) login(typ authv1.AccountType) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in struct {
			Email    string `json:"email" binding:"required"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			badRequest(c, "email and password are required")
			return
		}
		r, err := h.client.Login(c.Request.Context(), &authv1.LoginRequest{Type: typ, Email: in.Email, Password: in.Password})
		if err != nil {
			fail(c, err)
			return
		}
		h.respond(c, stdhttp.StatusOK, "Login OK", r)
	}
}

func (h *Auth) refresh(c *gin.Context) {
	token, _ := c.Cookie(refreshCookie)
	if token == "" {
		var in struct {
			RefreshToken string `json:"refreshToken"`
		}
		_ = c.ShouldBindJSON(&in) // an absent or malformed body simply means "no token"
		token = in.RefreshToken
	}
	if token == "" {
		fail(c, errs.Unauthenticated("TOKEN_INVALID", "a refresh token is required (cookie or body)"))
		return
	}
	r, err := h.client.Refresh(c.Request.Context(), &authv1.RefreshRequest{RefreshToken: token})
	if err != nil {
		if errs.CodeOf(err) == "TOKEN_REUSED" || errs.CodeOf(err) == "TOKEN_INVALID" {
			h.clearRefreshCookie(c)
		}
		fail(c, err)
		return
	}
	h.respond(c, stdhttp.StatusOK, "Token refreshed", r)
}

func (h *Auth) logout(c *gin.Context) {
	if _, err := h.client.Logout(c.Request.Context(), &authv1.LogoutRequest{}); err != nil {
		fail(c, err)
		return
	}
	h.clearRefreshCookie(c)
	ok(c, stdhttp.StatusOK, "Logout OK", nil)
}

func (h *Auth) newUser(c *gin.Context) {
	var in struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "email is required")
		return
	}
	if _, err := h.client.RequestEmailVerification(c.Request.Context(), &authv1.RequestEmailVerificationRequest{Email: in.Email}); err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Verification email sent", nil)
}

func (h *Auth) welcomeBack(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		badRequest(c, "token is required")
		return
	}
	r, err := h.client.VerifyEmail(c.Request.Context(), &authv1.VerifyEmailRequest{Token: token})
	if err != nil {
		fail(c, err)
		return
	}
	h.respond(c, stdhttp.StatusOK, "Email verified", r)
}

func (h *Auth) setPassword(c *gin.Context) {
	var in struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "password is required")
		return
	}
	if _, err := h.client.SetPassword(c.Request.Context(), &authv1.SetPasswordRequest{Password: in.Password}); err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Password set", nil)
}

// viewProfile serves /profile/viewOwn and /profile/viewAny. With the "any" possession an accountId query selects
// another account; with "own" it is ignored — the caller can only ever see themselves.
func (h *Auth) viewProfile(c *gin.Context) {
	id := ""
	if p, _ := c.Get(ctxPossession); p == rbac.Any {
		id = c.Query("accountId")
	}
	a, err := h.client.GetAccount(c.Request.Context(), &authv1.GetAccountRequest{AccountId: id})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Profile", accountOf(a))
}

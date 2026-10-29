package usecase

import (
	"time"

	"github.com/an-vt/ecom/auth-service/internal/domain"
)

// AccountView is an account as exposed to callers (never the password hash).
type AccountView struct {
	ID          string
	Type        string // SHOP | USER
	Email       string
	Name        string
	Status      string
	Verified    bool
	HasPassword bool
	Roles       []string
}

func viewOf(a *domain.Account) AccountView {
	return AccountView{ID: a.ID(), Type: string(a.Type()), Email: a.Email().String(), Name: a.Name(), Status: string(a.Status()),
		Verified: a.Verified(), HasPassword: a.HasPassword(), Roles: a.Roles()}
}

// Tokens are the credentials of a session.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
	SessionID    string
}

// AuthResult is returned by every use case that starts or renews a session.
type AuthResult struct {
	Account AccountView
	Tokens  Tokens
}

package usecase

import (
	"context"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/jwtx"
)

// ApiKeyCheck is the result of validating an x-api-key.
type ApiKeyCheck struct {
	Valid       bool
	Permissions []string
}

// ValidateApiKey checks a client application key (monolith checkAuth.apiKey).
type ValidateApiKey struct{ d *Deps }

// Execute looks the key up by hash. Unknown and inactive keys are simply not valid; permissions are only
// revealed for valid keys.
func (u ValidateApiKey) Execute(ctx context.Context, key string) (ApiKeyCheck, error) {
	if key == "" {
		return ApiKeyCheck{}, nil
	}
	k, err := u.d.ApiKeys.FindByHash(ctx, domain.HashToken(key))
	if err != nil {
		return ApiKeyCheck{}, err
	}
	if k == nil || !k.Active {
		return ApiKeyCheck{}, nil
	}
	return ApiKeyCheck{Valid: true, Permissions: append([]string(nil), k.Permissions...)}, nil
}

// GetJwks publishes the access-token verification keys.
type GetJwks struct{ d *Deps }

// Execute returns the JWKS.
func (u GetJwks) Execute() jwtx.JWKS { return u.d.Signer.JWKS() }

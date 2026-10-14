package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
)

var ErrInvalidToken = errors.New("invalid access token")

type Config struct {
	Issuer   string
	JWKSURL  string
	Audience string
}

type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewVerifier fetches the keys lazily from JWKSURL and refetches them when a token is signed with an unknown key.
// The JWKS URL is separate from the issuer because inside Docker the service reaches Keycloak by another host name than the one in iss.
func NewVerifier(cfg Config) (*Verifier, error) {
	switch {
	case cfg.Issuer == "":
		return nil, errors.New("auth issuer is required")
	case cfg.JWKSURL == "":
		return nil, errors.New("auth JWKS URL is required")
	case cfg.Audience == "":
		return nil, errors.New("auth audience is required")
	}

	// The key set keeps this context for its background fetches, so it must outlive any single request.
	keys := oidc.NewRemoteKeySet(context.Background(), cfg.JWKSURL)
	return &Verifier{verifier: oidc.NewVerifier(cfg.Issuer, keys, &oidc.Config{
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: []string{oidc.RS256},
	})}, nil
}

type accessTokenClaims struct {
	AuthorizedParty string `json:"azp"`
	ProviderID      string `json:"provider_id"`
	RealmAccess     struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// Verify checks signature, iss, aud and exp, then maps the claims to a Principal.
func (v *Verifier) Verify(ctx context.Context, raw string) (Principal, error) {
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	var claims accessTokenClaims
	if err := token.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	principal := Principal{ClientID: claims.AuthorizedParty, ProviderID: claims.ProviderID}
	for _, role := range claims.RealmAccess.Roles {
		if r := Role(role); r == RoleProvider || r == RoleInternal {
			principal.Roles = append(principal.Roles, r)
		}
	}
	// Without its provider id a provider token could not be tied to any transaction.
	if principal.Has(RoleProvider) && principal.ProviderID == "" {
		return Principal{}, fmt.Errorf("%w: provider token without provider_id", ErrInvalidToken)
	}
	return principal, nil
}

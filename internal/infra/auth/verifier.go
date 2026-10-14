package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

var ErrInvalidToken = errors.New("invalid access token")

const jwksTimeout = 10 * time.Second

type Config struct {
	Issuer   string
	JWKSURL  string
	Audience string
}

type Verifier struct {
	jwksURL   string
	client    *http.Client
	transport *http.Transport
	verifier  *oidc.IDTokenVerifier
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: transport, Timeout: jwksTimeout}
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), cfg.JWKSURL)
	return &Verifier{jwksURL: cfg.JWKSURL, client: client, transport: transport, verifier: oidc.NewVerifier(cfg.Issuer, keys, &oidc.Config{
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: []string{oidc.RS256},
	})}, nil
}

// Close drops the idle connections to the key server; the verifier keeps working and reconnects if used again.
func (v *Verifier) Close() {
	v.transport.CloseIdleConnections()
}

// CheckKeys fetches the key set once, so an unreachable or empty JWKS stops the start instead of rejecting every token later.
func (v *Verifier) CheckKeys(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch JWKS: status %d", response.StatusCode)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(response.Body).Decode(&set); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}
	if len(set.Keys) == 0 {
		return errors.New("JWKS has no keys")
	}
	return nil
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

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	testIssuer   = "http://keycloak.test/realms/wagering"
	testAudience = "wagering-service"
)

type signingKey struct {
	id      string
	private *rsa.PrivateKey
}

func newSigningKey(t *testing.T, id string) signingKey {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return signingKey{id: id, private: private}
}

// jwksServer publishes the public half of whatever keys it currently holds, like Keycloak after a rotation.
type jwksServer struct {
	mu   sync.Mutex
	keys []signingKey
	hits int
}

func (s *jwksServer) publish(keys ...signingKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
}

func (s *jwksServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	set := jose.JSONWebKeySet{}
	for _, k := range s.keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: &k.private.PublicKey, KeyID: k.id, Algorithm: "RS256", Use: "sig"})
	}
	_ = json.NewEncoder(w).Encode(set)
}

type tokenClaims struct {
	jwt.Claims
	AuthorizedParty string `json:"azp,omitempty"`
	ProviderID      string `json:"provider_id,omitempty"`
	RealmAccess     *struct {
		Roles []string `json:"roles"`
	} `json:"realm_access,omitempty"`
}

func claimsFor(clientID, providerID string, roles ...string) tokenClaims {
	now := time.Now()
	c := tokenClaims{
		Claims: jwt.Claims{
			Issuer: testIssuer, Audience: jwt.Audience{testAudience}, Subject: "service-account-" + clientID,
			IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
		AuthorizedParty: clientID,
		ProviderID:      providerID,
	}
	c.RealmAccess = &struct {
		Roles []string `json:"roles"`
	}{Roles: roles}
	return c
}

func sign(t *testing.T, key signingKey, claims tokenClaims) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key.private},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", key.id),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

func newTestVerifier(t *testing.T, keys ...signingKey) (*Verifier, *jwksServer) {
	t.Helper()
	jwks := &jwksServer{}
	jwks.publish(keys...)
	server := httptest.NewServer(jwks)
	t.Cleanup(server.Close)

	verifier, err := NewVerifier(Config{Issuer: testIssuer, JWKSURL: server.URL, Audience: testAudience})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return verifier, jwks
}

func TestVerifyMapsClaimsToPrincipal(t *testing.T) {
	key := newSigningKey(t, "key-1")
	verifier, _ := newTestVerifier(t, key)

	provider, err := verifier.Verify(context.Background(), sign(t, key, claimsFor("provider-a", "provider-a", "provider", "offline_access")))
	if err != nil {
		t.Fatalf("provider token: %v", err)
	}
	if provider.ClientID != "provider-a" || provider.ProviderID != "provider-a" || !provider.Has(RoleProvider) || provider.Has(RoleInternal) {
		t.Errorf("provider principal = %+v", provider)
	}

	internal, err := verifier.Verify(context.Background(), sign(t, key, claimsFor("wallet-service", "", "internal")))
	if err != nil {
		t.Fatalf("internal token: %v", err)
	}
	if !internal.Has(RoleInternal) || internal.Has(RoleProvider) {
		t.Errorf("internal principal = %+v", internal)
	}
}

func TestVerifyRejectsInvalidTokens(t *testing.T) {
	key := newSigningKey(t, "key-1")
	verifier, _ := newTestVerifier(t, key)
	valid := func() tokenClaims { return claimsFor("provider-a", "provider-a", "provider") }

	expired := valid()
	expired.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	otherIssuer := valid()
	otherIssuer.Issuer = "http://attacker.test/realms/wagering"
	otherAudience := valid()
	otherAudience.Audience = jwt.Audience{"account"}
	noProviderID := valid()
	noProviderID.ProviderID = ""

	validToken := sign(t, key, valid())
	tampered := validToken[:len(validToken)-4] + "AAAA"

	tests := map[string]string{
		"expired":                      sign(t, key, expired),
		"another issuer":               sign(t, key, otherIssuer),
		"another audience":             sign(t, key, otherAudience),
		"signed by an unknown key":     sign(t, newSigningKey(t, "key-1"), valid()),
		"tampered signature":           tampered,
		"provider without provider_id": sign(t, key, noProviderID),
		"unsigned":                     unsignedToken(t, valid()),
		"not a jwt":                    "not-a-token",
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func unsignedToken(t *testing.T, claims tokenClaims) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// {"alg":"none","typ":"JWT"}
	return "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func TestVerifyFollowsKeyRotation(t *testing.T) {
	oldKey, newKey := newSigningKey(t, "key-1"), newSigningKey(t, "key-2")
	verifier, jwks := newTestVerifier(t, oldKey)
	claims := claimsFor("provider-a", "provider-a", "provider")

	if _, err := verifier.Verify(context.Background(), sign(t, oldKey, claims)); err != nil {
		t.Fatalf("token signed with the published key: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), sign(t, oldKey, claims)); err != nil {
		t.Fatalf("second token: %v", err)
	}
	if jwks.hits != 1 {
		t.Errorf("JWKS fetched %d times for two tokens with the same key, want 1 (cached)", jwks.hits)
	}

	jwks.publish(newKey)
	if _, err := verifier.Verify(context.Background(), sign(t, newKey, claims)); err != nil {
		t.Errorf("token signed with the rotated key: %v", err)
	}
}

func TestNewVerifierRequiresConfiguration(t *testing.T) {
	for name, cfg := range map[string]Config{
		"issuer":   {JWKSURL: "x", Audience: "x"},
		"jwks url": {Issuer: "x", Audience: "x"},
		"audience": {Issuer: "x", JWKSURL: "x"},
	} {
		if _, err := NewVerifier(cfg); err == nil {
			t.Errorf("missing %s: expected an error", name)
		}
	}
}

func TestPrincipalVisibility(t *testing.T) {
	providerA := Principal{ProviderID: "provider-a", Roles: []Role{RoleProvider}}
	internal := Principal{Roles: []Role{RoleInternal}}

	tests := []struct {
		name      string
		principal Principal
		provider  string
		want      bool
	}{
		{"provider sees its own", providerA, "provider-a", true},
		{"provider does not see another", providerA, "provider-b", false},
		{"provider does not see internal transactions", providerA, "", false},
		{"internal sees any provider", internal, "provider-b", true},
		{"internal sees internal transactions", internal, "", true},
		{"anonymous sees nothing", Principal{}, "provider-a", false},
	}
	for _, tt := range tests {
		if got := tt.principal.CanSee(tt.provider); got != tt.want {
			t.Errorf("%s: CanSee = %v, want %v", tt.name, got, tt.want)
		}
	}
}

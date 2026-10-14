package auth

import (
	"context"
	"slices"
)

type Role string

const (
	RoleProvider Role = "provider"
	RoleInternal Role = "internal"
)

// Principal is the caller proven by the access token.
type Principal struct {
	ClientID   string
	ProviderID string
	Roles      []Role
}

func (p Principal) Has(role Role) bool {
	return slices.Contains(p.Roles, role)
}

// CanSee reports whether the caller may read a transaction of providerID.
func (p Principal) CanSee(providerID string) bool {
	return p.Has(RoleInternal) || (p.Has(RoleProvider) && providerID != "" && p.ProviderID == providerID)
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

package auth

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTokenClaimsFromMapClaims_Success(t *testing.T) {
	mc := jwt.MapClaims{
		"sub":   "user-123",
		"scope": "leartechapi",
		"ext":   map[string]interface{}{"Permissions": []any{"User"}},
	}
	claims, err := NewTokenClaimsFromMapClaims(mc)
	require.NoError(t, err)
	assert.Equal(t, "user-123", claims.UserID)
	assert.Equal(t, Scopes{ScopeAPI}, claims.Scopes)
	assert.Equal(t, Permissions{PermUser}, claims.Permissions)
}

func TestNewTokenClaimsFromMapClaims_ServiceTokenWithoutPerms(t *testing.T) {
	mc := jwt.MapClaims{
		"sub":   "service-account",
		"scope": "leartechapi.internal_services",
	}
	claims, err := NewTokenClaimsFromMapClaims(mc)
	require.NoError(t, err)
	assert.Equal(t, "service-account", claims.UserID)
	assert.Empty(t, claims.Permissions, "service tokens carry no permissions")
}

func TestNewTokenClaimsFromMapClaims_RejectsMissingSub(t *testing.T) {
	_, err := NewTokenClaimsFromMapClaims(jwt.MapClaims{"scope": "leartechapi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sub")
}

// Hydra-issued JWT access tokens carry scopes under `scp` (an array,
// per JWT IANA registry), not `scope` (string, OAuth2 introspection
// convention). Failing to read `scp` was the root cause of the staging
// fleet-test failure on 2026-05-18 — middleware activated correctly,
// then rejected every valid PKCE token with "missing 'scope' claim".
func TestNewTokenClaimsFromMapClaims_HydraScpArrayForm(t *testing.T) {
	// Real staging-issued token shape (PKCE user login → frontend-services
	// client). aud is multi-valued (RFC 8707), scopes are openid+offline.
	mc := jwt.MapClaims{
		"sub": "user-test-001",
		"aud": []interface{}{
			"leartech-rust-service-template",
			"leartech-go-service-template",
			"leartech-dotnet-service-template",
			"leartech-auth-service",
		},
		"scp": []interface{}{"openid", "offline"},
		"ext": map[string]interface{}{"Permissions": []any{"SuperAdmin"}},
	}
	claims, err := NewTokenClaimsFromMapClaims(mc)
	require.NoError(t, err)
	assert.Equal(t, "user-test-001", claims.UserID)
	assert.Equal(t, Scopes{Scope("openid"), Scope("offline")}, claims.Scopes)
}

func TestNewTokenClaimsFromMapClaims_PrefersScpOverScope(t *testing.T) {
	// If both are present (e.g. legacy introspection adapter emitting
	// both), `scp` wins because it's the JWT IANA standard.
	mc := jwt.MapClaims{
		"sub":   "u",
		"scp":   []interface{}{"leartechapi"},
		"scope": "leartechapi.internal_services",
	}
	claims, err := NewTokenClaimsFromMapClaims(mc)
	require.NoError(t, err)
	assert.Equal(t, Scopes{ScopeAPI}, claims.Scopes)
}

func TestNewTokenClaimsFromMapClaims_MissingScopeIsOK(t *testing.T) {
	// Tokens with no scope claim parse cleanly. Whether they're allowed
	// is the caller's decision via permissions check, not this layer's.
	claims, err := NewTokenClaimsFromMapClaims(jwt.MapClaims{"sub": "u"})
	require.NoError(t, err)
	assert.Equal(t, "u", claims.UserID)
	assert.Empty(t, claims.Scopes)
}

func TestNewTokenClaimsFromMapClaims_RejectsBadScopeType(t *testing.T) {
	_, err := NewTokenClaimsFromMapClaims(jwt.MapClaims{"sub": "u", "scope": 42})
	require.Error(t, err)
}

func TestExtractPermissionsFromClaims_NoExt(t *testing.T) {
	got, err := extractPermissionsFromClaims(jwt.MapClaims{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestExtractPermissionsFromClaims_ExtNotMap(t *testing.T) {
	got, err := extractPermissionsFromClaims(jwt.MapClaims{"ext": "not-a-map"})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestExtractPermissionsFromClaims_MissingPermissionsKey(t *testing.T) {
	got, err := extractPermissionsFromClaims(jwt.MapClaims{"ext": map[string]interface{}{}})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestNewTokenClaimsFromMapClaims_IdentityClaims(t *testing.T) {
	mc := jwt.MapClaims{
		"sub":   "user-123",
		"scope": "leartechapi",
		"ext": map[string]interface{}{
			"tenant_id":   "00000000-0000-0000-0000-000000000001",
			"user_role":   "platform_admin",
			"external_id": "user-test-platform",
		},
	}
	claims, err := NewTokenClaimsFromMapClaims(mc)
	require.NoError(t, err)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", claims.TenantID)
	assert.Equal(t, "platform_admin", claims.UserRole)
	assert.Equal(t, "user-test-platform", claims.ExternalID)
}

func TestNewTokenClaimsFromMapClaims_IdentityClaimsAbsent(t *testing.T) {
	// Service token (no ext identity) → empty strings, not an error.
	claims, err := NewTokenClaimsFromMapClaims(jwt.MapClaims{"sub": "svc-1", "scope": "internalservice"})
	require.NoError(t, err)
	assert.Empty(t, claims.TenantID)
	assert.Empty(t, claims.UserRole)
	assert.Empty(t, claims.ExternalID)
}

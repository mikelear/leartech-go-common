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

func TestNewTokenClaimsFromMapClaims_RejectsMissingScope(t *testing.T) {
	_, err := NewTokenClaimsFromMapClaims(jwt.MapClaims{"sub": "u"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope")
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

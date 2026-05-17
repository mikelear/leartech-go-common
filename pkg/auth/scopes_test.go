package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewScopesFromAny(t *testing.T) {
	t.Run("space-separated string (OAuth2 introspection form)", func(t *testing.T) {
		got, err := newScopesFromAny("leartechapi leartechapi.internal_services")
		require.NoError(t, err)
		assert.Equal(t, Scopes{ScopeAPI, ScopeInternalServices}, got)
	})
	t.Run("single scope string", func(t *testing.T) {
		got, err := newScopesFromAny("leartechapi")
		require.NoError(t, err)
		assert.Equal(t, Scopes{ScopeAPI}, got)
	})
	t.Run("array form (Hydra JWT scp claim)", func(t *testing.T) {
		// Hydra emits scp as []interface{} via jwt.MapClaims after JSON
		// unmarshal — the same shape as the staging fleet-test token.
		got, err := newScopesFromAny([]interface{}{"openid", "offline"})
		require.NoError(t, err)
		assert.Equal(t, Scopes{Scope("openid"), Scope("offline")}, got)
	})
	t.Run("nil input → empty scopes, no error", func(t *testing.T) {
		// Tokens without any scope claim are valid; callers decide
		// via permissions whether unscoped tokens are allowed.
		got, err := newScopesFromAny(nil)
		require.NoError(t, err)
		assert.Equal(t, Scopes{}, got)
	})
	t.Run("non-string array element rejected", func(t *testing.T) {
		_, err := newScopesFromAny([]interface{}{"openid", 42})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-string element")
	})
	t.Run("unsupported type rejected", func(t *testing.T) {
		_, err := newScopesFromAny([]string{"x"})
		require.Error(t, err)
	})
}

func TestScopes_HasInternalService(t *testing.T) {
	assert.True(t, Scopes{ScopeInternalServices}.HasInternalService())
	assert.False(t, Scopes{ScopeAPI}.HasInternalService())
	assert.False(t, Scopes{}.HasInternalService())
}

func TestScopes_HasAPI(t *testing.T) {
	assert.True(t, Scopes{ScopeAPI}.HasAPI())
	assert.True(t, Scopes{ScopeAPI, ScopeInternalServices}.HasAPI())
	assert.False(t, Scopes{ScopeInternalServices}.HasAPI())
	assert.False(t, Scopes{}.HasAPI())
}

func TestScopes_IsScoped(t *testing.T) {
	tests := []struct {
		name     string
		have     Scopes
		required Scopes
		want     bool
	}{
		{"empty have → denied", Scopes{}, Scopes{ScopeAPI}, false},
		{"empty required → permitted", Scopes{ScopeAPI}, Scopes{}, true},
		{"match", Scopes{ScopeAPI}, Scopes{ScopeAPI}, true},
		{"one-of match", Scopes{ScopeAPI}, Scopes{ScopeAPI, ScopeInternalServices}, true},
		{"no overlap", Scopes{ScopeAPI}, Scopes{ScopeInternalServices}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.have.IsScoped(tc.required))
		})
	}
}

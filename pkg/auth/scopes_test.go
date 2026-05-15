package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewScopesFromAny(t *testing.T) {
	t.Run("space-separated string", func(t *testing.T) {
		got, err := newScopesFromAny("leartechapi leartechapi.internal_services")
		require.NoError(t, err)
		assert.Equal(t, Scopes{ScopeAPI, ScopeInternalServices}, got)
	})
	t.Run("single scope", func(t *testing.T) {
		got, err := newScopesFromAny("leartechapi")
		require.NoError(t, err)
		assert.Equal(t, Scopes{ScopeAPI}, got)
	})
	t.Run("non-string input", func(t *testing.T) {
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

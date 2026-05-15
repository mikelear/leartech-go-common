package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPermissionsFromAny(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    Permissions
		wantErr bool
	}{
		{"single string", "User", Permissions{PermUser}, false},
		{"string slice", []string{"User", "Admin"}, Permissions{PermUser, PermAdmin}, false},
		{"any slice of strings", []any{"User", "Admin"}, Permissions{PermUser, PermAdmin}, false},
		{"any slice with non-string", []any{"User", 42}, nil, true},
		{"unsupported type", 42, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newPermissionsFromAny(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPermissions_IsPermitted(t *testing.T) {
	tests := []struct {
		name     string
		have     Permissions
		required Permissions
		want     bool
	}{
		{"empty required → always permitted", Permissions{}, nil, true},
		{"empty have, non-empty required → denied", nil, Permissions{PermUser}, false},
		{"single match", Permissions{PermUser}, Permissions{PermUser}, true},
		{"one of many matches", Permissions{PermAdmin}, Permissions{PermUser, PermAdmin}, true},
		{"no overlap → denied", Permissions{PermUser}, Permissions{PermAdmin}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.have.IsPermitted(tc.required))
		})
	}
}

package auth

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

// TokenClaims represents the decoded claims from a Hydra JWT.
type TokenClaims struct {
	UserID      string
	Permissions Permissions
	Scopes      Scopes
}

// NewTokenClaimsFromMapClaims extracts leartech-specific claims from a JWT.
func NewTokenClaimsFromMapClaims(mc jwt.MapClaims) (*TokenClaims, error) {
	// Subject (required)
	var userID string
	if sub, ok := mc["sub"].(string); ok && sub != "" {
		userID = sub
	}
	if userID == "" {
		return nil, fmt.Errorf("token missing 'sub' claim")
	}

	// Scopes (required — space-separated string in "scope" claim)
	scopes, err := newScopesFromAny(mc["scope"])
	if err != nil {
		return nil, fmt.Errorf("failed to parse 'scope' claim: %w", err)
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("token missing 'scope' claim")
	}

	// Permissions (optional — present in user tokens, absent in service tokens)
	permissions, err := extractPermissionsFromClaims(mc)
	if err != nil {
		return nil, err
	}
	if len(permissions) == 0 {
		log.Debug().Msg("no permissions found in claims (expected for service tokens)")
	}

	return &TokenClaims{
		UserID:      userID,
		Permissions: permissions,
		Scopes:      scopes,
	}, nil
}

// extractPermissionsFromClaims reads ext.Permissions from the JWT claims.
// Hydra places custom claims under the "ext" key.
func extractPermissionsFromClaims(mc jwt.MapClaims) (Permissions, error) {
	ext, ok := mc["ext"]
	if !ok {
		return nil, nil
	}

	extMap, ok := ext.(map[string]interface{})
	if !ok {
		return nil, nil
	}

	permsAny, ok := extMap["Permissions"]
	if !ok {
		return nil, nil
	}

	return newPermissionsFromAny(permsAny)
}

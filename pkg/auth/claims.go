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

	// Scopes — prefer `scp` (JWT IANA standard for JWT-format scopes;
	// what Ory Hydra emits on access tokens as an array), fall back to
	// `scope` (OAuth2 introspection convention; space-separated string).
	// Empty/missing scopes are NOT an error here — the caller decides
	// via `Middleware(requiredPerms)` whether unscoped tokens are allowed.
	// Mirrors rust + dotnet templates' AuthLayer behaviour which accept
	// any audience-bound valid-signature token and leave scope checks
	// to the handler.
	scopesAny := mc["scp"]
	if scopesAny == nil {
		scopesAny = mc["scope"]
	}
	scopes, err := newScopesFromAny(scopesAny)
	if err != nil {
		return nil, fmt.Errorf("failed to parse scope claim: %w", err)
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

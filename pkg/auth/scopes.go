package auth

import (
	"fmt"
	"slices"
	"strings"

	"github.com/rs/zerolog/log"
)

// Scope represents an OAuth2 scope string.
type Scope string

// Standard scopes for leartech services.
const (
	ScopeAPI              Scope = "leartechapi"
	ScopeInternalServices Scope = "leartechapi.internal_services"
)

// Scopes is a list of OAuth2 scopes from a token.
type Scopes []Scope

// NewScopes builds a Scopes list from string values — for sourcing a required
// scope from config/env (e.g. LEARTECH_AUTH_REQUIRED_SCOPES) rather than a
// hard-coded helper, so partner/external scopes stay config, not code. Blank
// entries are dropped.
func NewScopes(ss []string) Scopes {
	scopes := make(Scopes, 0, len(ss))
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			scopes = append(scopes, Scope(s))
		}
	}
	return scopes
}

// newScopesFromAny parses the scope claim from JWT MapClaims. Handles both
// OAuth2's space-separated string form (`"openid offline"`) and JWT IANA's
// array form (`["openid", "offline"]`). Hydra emits the array form on access
// tokens (per the `scp` claim convention from RFC 8693 / IANA JWT registry);
// older OAuth2 introspection endpoints emit the string form. Accept both.
//
// nil input → empty scopes (not an error). Tokens with no scope claim at all
// are valid; the caller decides via permission checks whether that's OK.
func newScopesFromAny(scopesAny any) (Scopes, error) {
	if scopesAny == nil {
		return Scopes{}, nil
	}
	var scopes Scopes
	switch v := scopesAny.(type) {
	case string:
		for _, s := range strings.Fields(v) {
			scopes = append(scopes, Scope(s))
		}
	case []interface{}:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("scope array contains non-string element: %T", item)
			}
			scopes = append(scopes, Scope(s))
		}
	default:
		return nil, fmt.Errorf("invalid scopes type: %T", scopesAny)
	}
	return scopes, nil
}

// HasInternalService returns true if the token has the internal-services scope.
func (s Scopes) HasInternalService() bool {
	return s.IsScoped(Scopes{ScopeInternalServices})
}

// HasAPI returns true if the token has the API scope.
func (s Scopes) HasAPI() bool {
	return s.IsScoped(Scopes{ScopeAPI})
}

// IsScoped returns true if at least one of the required scopes is present.
func (s Scopes) IsScoped(requiredScopes Scopes) bool {
	if len(s) == 0 {
		log.Debug().Msg("scopes is empty")
		return false
	}
	if len(requiredScopes) == 0 {
		return true
	}
	for _, have := range s {
		if slices.Contains(requiredScopes, have) {
			return true
		}
	}
	return false
}

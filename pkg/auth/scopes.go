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

func newScopesFromAny(scopesAny any) (Scopes, error) {
	var scopes Scopes
	switch v := scopesAny.(type) {
	case string:
		for _, s := range strings.Fields(v) {
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

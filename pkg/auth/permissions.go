package auth

import (
	"fmt"
	"slices"
)

// Permission represents a user-level permission from ext.Permissions.
type Permission string

// Standard permissions for leartech services.
// Services can define additional permissions as needed.
const (
	PermUser  Permission = "User"
	PermAdmin Permission = "Admin"
)

// Permissions is a list of user permissions from a token.
type Permissions []Permission

func newPermissionsFromAny(permsAny any) (Permissions, error) {
	var permissions Permissions
	switch v := permsAny.(type) {
	case string:
		permissions = append(permissions, Permission(v))
	case []string:
		for _, s := range v {
			permissions = append(permissions, Permission(s))
		}
	case []any:
		for _, s := range v {
			if str, ok := s.(string); ok {
				permissions = append(permissions, Permission(str))
			} else {
				return nil, fmt.Errorf("invalid permission type: %T", s)
			}
		}
	default:
		return nil, fmt.Errorf("invalid permissions type: %T", permsAny)
	}
	return permissions, nil
}

// IsPermitted returns true if at least one of the required permissions is present.
func (p Permissions) IsPermitted(required Permissions) bool {
	if len(required) == 0 {
		return true
	}
	if len(p) == 0 {
		return false
	}
	for _, have := range p {
		if slices.Contains(required, have) {
			return true
		}
	}
	return false
}

// Package auth provides service authentication: inbound JWT middleware
// (validate caller tokens via JWKS) and outbound token management
// (client_credentials flow for service-to-service calls).
//
// Usage:
//
//	client, err := auth.NewServiceClient(ctx, auth.Config{
//	    ServerURL:    os.Getenv("LEARTECH_AUTH_SERVER_URL"),
//	    ClientID:     os.Getenv("LEARTECH_AUTH_CLIENT_ID"),
//	    ClientSecret: os.Getenv("LEARTECH_AUTH_CLIENT_SECRET"),
//	})
//
//	// Inbound: protect endpoints
//	router.GET("/api/things", client.Middleware(auth.Permissions{"User"}), handler)
//
//	// Outbound: call another service
//	httpClient := client.HTTPClient()
package auth

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// TokenClaimsKey is the gin context key where decoded claims are stored
// after successful middleware validation.
const TokenClaimsKey = "leartech_auth_claims" //nolint:gosec // G101 false positive: context key name, not a credential

// AuthorizationHeaderKey is the HTTP header key for Bearer tokens.
const AuthorizationHeaderKey = "Authorization"

// TokenGetter retrieves auth tokens for outbound service-to-service calls.
type TokenGetter interface {
	// GetAuthToken returns the service's current token, refreshing if expired.
	GetAuthToken(ctx context.Context) (*string, error)
	// SetAuthHeader attaches the service's current token to the request.
	SetAuthHeader(ctx context.Context, req *http.Request) error
}

// ServiceAuthClient provides both inbound middleware and outbound token management.
//
// Auth is mandatory. There is no IsDisabled / noop / pass-through path — a
// constructed client always enforces JWKS signature validation, audience
// binding, and permission/scope checks on every request. To use it, the
// caller must supply a full Config (ServerURL + ClientID + ClientSecret +
// Audience); missing config is a construction error.
type ServiceAuthClient interface {
	TokenGetter
	// Middleware validates the caller's JWT and checks required permissions.
	Middleware(requiredPerms Permissions) gin.HandlerFunc
	// RequireScopes validates the JWT and requires AT LEAST ONE of the given
	// scopes (any-of, not all-of — see the ServiceClient impl for rationale) —
	// config-driven s2s auth (source `required` from config, e.g.
	// NewScopes(cfg.RequiredScopes)).
	RequireScopes(required Scopes) gin.HandlerFunc
	// GetRequestTokenClaimsFromGinContext returns the caller's decoded claims.
	GetRequestTokenClaimsFromGinContext(gc *gin.Context) (*TokenClaims, error)
	// HTTPClient returns an http.Client that auto-attaches the service token.
	HTTPClient() *http.Client
	// Ping checks that the authorization server is reachable.
	Ping(ctx context.Context) error
}

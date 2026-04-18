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
const TokenClaimsKey = "leartech_auth_claims"

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
type ServiceAuthClient interface {
	TokenGetter
	// IsDisabled returns true if auth is disabled (e.g. local dev).
	IsDisabled() bool
	// Middleware validates the caller's JWT and checks required permissions.
	Middleware(requiredPerms Permissions) gin.HandlerFunc
	// GetRequestTokenClaimsFromGinContext returns the caller's decoded claims.
	GetRequestTokenClaimsFromGinContext(gc *gin.Context) (*TokenClaims, error)
	// HTTPClient returns an http.Client that auto-attaches the service token.
	HTTPClient() *http.Client
	// Ping checks that the authorization server is reachable.
	Ping(ctx context.Context) error
}

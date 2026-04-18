package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// ServiceClient implements ServiceAuthClient using Hydra's OAuth2 endpoints.
type ServiceClient struct {
	cfg         Config
	jwksKeyFunc keyfunc.Keyfunc
	tokenSource oauth2.TokenSource
	healthURL   string
	httpClient  *http.Client
}

// NewServiceClient creates a ServiceAuthClient that authenticates with Hydra
// and validates incoming JWTs via JWKS.
func NewServiceClient(ctx context.Context, cfg Config) (ServiceAuthClient, error) {
	if cfg.ServerURL == "" {
		return &noopClient{}, nil
	}

	hydraBaseURL, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL: %w", err)
	}

	// Client credentials flow for outbound service-to-service auth
	oauth2Config := clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     hydraBaseURL.ResolveReference(&url.URL{Path: "/oauth2/token"}).String(),
		Scopes:       []string{string(ScopeInternalServices)},
		AuthStyle:    oauth2.AuthStyleInParams,
	}

	// JWKS for validating inbound tokens
	jwksURL := hydraBaseURL.ResolveReference(&url.URL{Path: "/.well-known/jwks.json"}).String()
	jwksKF, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS keyfunc from %s: %w", jwksURL, err)
	}

	httpClient := oauth2Config.Client(ctx)
	return &ServiceClient{
		cfg:         cfg,
		jwksKeyFunc: jwksKF,
		tokenSource: oauth2Config.TokenSource(ctx),
		httpClient:  httpClient,
		healthURL:   hydraBaseURL.ResolveReference(&url.URL{Path: "/health/ready"}).String(),
	}, nil
}

// IsDisabled returns true if auth middleware is disabled (local dev).
func (c *ServiceClient) IsDisabled() bool {
	return c.cfg.DisableMiddleware
}

// GetAuthToken returns the current cached token, refreshing if expired.
func (c *ServiceClient) GetAuthToken(ctx context.Context) (*string, error) {
	token, err := c.tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}
	t := token.AccessToken
	return &t, nil
}

// SetAuthHeader attaches the Bearer token to the request.
func (c *ServiceClient) SetAuthHeader(ctx context.Context, req *http.Request) error {
	token, err := c.GetAuthToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set(AuthorizationHeaderKey, "Bearer "+*token)
	return nil
}

// HTTPClient returns an http.Client that auto-attaches the service token.
func (c *ServiceClient) HTTPClient() *http.Client {
	return c.httpClient
}

// Middleware validates the caller's JWT and checks permissions.
//
// Usage:
//
//	router.GET("/api/things", client.Middleware(auth.Permissions{"User"}), handler)
//	router.GET("/api/admin",  client.Middleware(auth.Permissions{"Admin"}), handler)
//	router.GET("/api/internal", client.Middleware(nil), handler) // any valid token
func (c *ServiceClient) Middleware(requiredPerms Permissions) gin.HandlerFunc {
	return func(gc *gin.Context) {
		if c.cfg.DisableMiddleware {
			gc.Next()
			return
		}

		tokenClaims, err := c.GetRequestTokenClaimsFromGinContext(gc)
		if err != nil {
			log.Debug().Err(err).Msg("failed to decode/verify token")
			gc.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		if c.isTokenAllowedAccess(requiredPerms, tokenClaims) {
			gc.Set(TokenClaimsKey, tokenClaims)
			gc.Next()
		} else {
			log.Debug().Msg("token not authorised for required permissions")
			gc.AbortWithStatus(http.StatusForbidden)
		}
	}
}

// isTokenAllowedAccess checks if the token has the required permissions.
// Internal service tokens (ScopeInternalServices) get full access.
// User tokens need the API scope AND the required permissions.
func (c *ServiceClient) isTokenAllowedAccess(requiredPerms Permissions, claims *TokenClaims) bool {
	return claims.Scopes.HasInternalService() ||
		(claims.Scopes.HasAPI() && claims.Permissions.IsPermitted(requiredPerms))
}

// GetRequestTokenClaimsFromGinContext extracts and decodes the JWT from
// the request. Returns cached claims if middleware already ran.
func (c *ServiceClient) GetRequestTokenClaimsFromGinContext(gc *gin.Context) (*TokenClaims, error) {
	// Check if a previous middleware already decoded the claims
	if existing, ok := gc.Get(TokenClaimsKey); ok {
		if tc, valid := existing.(*TokenClaims); valid {
			return tc, nil
		}
	}

	token, err := getTokenFromHeader(gc.GetHeader(AuthorizationHeaderKey))
	if err != nil {
		return nil, err
	}
	return c.decodeToken(token)
}

// Ping checks Hydra's health endpoint.
func (c *ServiceClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.healthURL, nil)
	if err != nil {
		return fmt.Errorf("building health request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	return nil
}

func (c *ServiceClient) decodeToken(tokenStr string) (*TokenClaims, error) {
	jwtToken, err := jwt.Parse(tokenStr, c.jwksKeyFunc.Keyfunc)
	if err != nil {
		return nil, fmt.Errorf("failed to parse/verify token: %w", err)
	}

	claims, ok := jwtToken.Claims.(jwt.MapClaims)
	if !ok || !jwtToken.Valid {
		return nil, fmt.Errorf("token is invalid")
	}

	return NewTokenClaimsFromMapClaims(claims)
}

func getTokenFromHeader(header string) (string, error) {
	if header == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", fmt.Errorf("invalid Authorization header format")
	}
	return parts[1], nil
}

// noopClient is returned when ServerURL is empty (local dev, no auth).
type noopClient struct{}

func (n *noopClient) IsDisabled() bool                                    { return true }
func (n *noopClient) GetAuthToken(_ context.Context) (*string, error)     { s := ""; return &s, nil }
func (n *noopClient) SetAuthHeader(_ context.Context, _ *http.Request) error { return nil }
func (n *noopClient) HTTPClient() *http.Client                            { return http.DefaultClient }
func (n *noopClient) Ping(_ context.Context) error                        { return nil }

func (n *noopClient) Middleware(_ Permissions) gin.HandlerFunc {
	return func(gc *gin.Context) { gc.Next() }
}

func (n *noopClient) GetRequestTokenClaimsFromGinContext(_ *gin.Context) (*TokenClaims, error) {
	return &TokenClaims{UserID: "dev-user", Scopes: Scopes{ScopeAPI}, Permissions: Permissions{PermAdmin}}, nil
}

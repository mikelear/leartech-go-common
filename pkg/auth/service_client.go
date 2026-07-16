package auth

import (
	"context"
	"errors"
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
//
// Fail-closed contract (auth-hardening A1):
//
//   - ServerURL, ClientID, ClientSecret, and Audience are ALL required. If any
//     is empty, this returns an error and the caller MUST NOT run — there is
//     no noop / pass-through / disabled fallback, and no configuration flag
//     that makes the middleware a runtime no-op.
//   - Callers cannot ignore the error and end up with an unauthenticated
//     service: the returned client validates JWKS signatures and RFC 8707
//     audience binding on every request.
func NewServiceClient(ctx context.Context, cfg Config) (ServiceAuthClient, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
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
	// §A-minimal (RFC 8707): request a per-service audience so Hydra binds the
	// minted token's `aud` to the target callee. Without this, client_credentials
	// tokens come back with aud=[] (the client's `audience` field is only an
	// allow-list, not a default), so any audience-validating callee fails open.
	// Empty TargetAudience = unset (legacy scope-only behaviour, no regression).
	if cfg.TargetAudience != "" {
		oauth2Config.EndpointParams = url.Values{"audience": {cfg.TargetAudience}}
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

// validateConfig enforces the fail-closed construction contract: every field
// that would otherwise leave the middleware in an unauthenticated state must
// be set. Returns a single error listing every missing field so operators see
// the whole gap in one boot log, instead of chasing one env-var at a time.
func validateConfig(cfg Config) error {
	var missing []string
	if cfg.ServerURL == "" {
		missing = append(missing, "LEARTECH_AUTH_SERVER_URL")
	}
	if cfg.ClientID == "" {
		missing = append(missing, "LEARTECH_AUTH_CLIENT_ID")
	}
	if cfg.ClientSecret == "" {
		missing = append(missing, "LEARTECH_AUTH_CLIENT_SECRET")
	}
	if cfg.Audience == "" {
		missing = append(missing, "LEARTECH_AUTH_AUDIENCE")
	}
	if len(missing) > 0 {
		return errors.New("auth: refusing to start — required config missing: " +
			strings.Join(missing, ", ") +
			" (auth is mandatory; there is no runtime disable path)")
	}
	return nil
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

// Middleware validates the caller's JWT and checks permissions. There is NO
// disable / bypass path — if the token is missing, mis-signed, mis-audienced
// or lacks the required permissions the request is rejected. Callers wanting
// unauthenticated infra routes (health, openapi, /.well-known/*) simply do
// not attach this middleware to those handlers.
//
// Usage:
//
//	router.GET("/api/things", client.Middleware(auth.Permissions{"User"}), handler)
//	router.GET("/api/admin",  client.Middleware(auth.Permissions{"Admin"}), handler)
//	router.GET("/api/internal", client.Middleware(nil), handler) // any valid token
func (c *ServiceClient) Middleware(requiredPerms Permissions) gin.HandlerFunc {
	return func(gc *gin.Context) {
		tokenClaims, err := c.GetRequestTokenClaimsFromGinContext(gc)
		if err != nil {
			log.Debug().Err(err).Msg("failed to decode/verify token")
			// RFC 9728 §5.1: point the client at the resource-metadata doc so
			// it can discover the authorisation server. No-op unless configured.
			if hint := wwwAuthenticateBearerHint(c.cfg); hint != "" {
				gc.Header("WWW-Authenticate", hint)
			}
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

// RequireScopes gates a route on the token carrying AT LEAST ONE of the given
// scope(s) — ANY-OF, not all-of. This matches our caller-type/tier scope model
// (leartechapi / leartechapi.internal_services / …external): a caller is one
// type, so a route listing several means "accept any of these caller types"
// (all-of would be unsatisfiable). True all-of (e.g. future capability scopes)
// would be a separate RequireAllScopes — don't overload this.
//
// Config-driven s2s auth: unlike Middleware (which lets any internal-services
// token through), this requires the SPECIFIC configured scope, so external/
// partner scopes stay config, not code — source `required` from config
// (e.g. RequireScopes(NewScopes(cfg.RequiredScopes))). Fail-closed: 401 on an
// invalid/absent token, 403 when none of the required scopes is present.
func (c *ServiceClient) RequireScopes(required Scopes) gin.HandlerFunc {
	return func(gc *gin.Context) {
		tokenClaims, err := c.GetRequestTokenClaimsFromGinContext(gc)
		if err != nil {
			log.Debug().Err(err).Msg("failed to decode/verify token")
			if hint := wwwAuthenticateBearerHint(c.cfg); hint != "" {
				gc.Header("WWW-Authenticate", hint)
			}
			gc.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		if !tokenClaims.Scopes.IsScoped(required) {
			log.Debug().Msg("token missing required scope(s)")
			gc.AbortWithStatus(http.StatusForbidden)
			return
		}
		gc.Set(TokenClaimsKey, tokenClaims)
		gc.Next()
	}
}

// isTokenAllowedAccess checks if the token has the required permissions.
//
// When `requiredPerms` is nil (the `Middleware(nil)` form, which says
// "any authenticated user"), any audience-validated signed token is
// allowed — matching rust + dotnet templates' AuthLayer behaviour.
// PKCE user tokens with only `openid offline` scopes (the SPA pattern)
// are accepted here.
//
// When `requiredPerms` is non-empty, the token must either:
//   - have the internal-services scope (full S2S access), OR
//   - have the API scope AND match at least one required permission
//
// Delegates to the package-level isTokenAllowedAccess (verifier.go) so the
// ServiceClient and Verifier middleware paths never drift. Kept as a method
// here because existing tests (service_client_test.go) exercise it through a
// *ServiceClient receiver.
func (c *ServiceClient) isTokenAllowedAccess(requiredPerms Permissions, claims *TokenClaims) bool {
	return isTokenAllowedAccess(requiredPerms, claims)
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
	defer func() { _ = resp.Body.Close() }()
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

	// RFC 8707 audience binding. Audience is guaranteed non-empty by
	// validateConfig at construction, so this always enforces — matching
	// rust + dotnet templates. There is no lenient / opt-out path.
	if err := validateAudience(claims, c.cfg.Audience); err != nil {
		return nil, fmt.Errorf("audience validation failed: %w", err)
	}

	return NewTokenClaimsFromMapClaims(claims)
}

// validateAudience checks the token's `aud` claim contains the expected
// audience. RFC 7519 §4.1.3 allows aud to be either a string or an
// array — handle both. Returns nil on match, error on mismatch.
func validateAudience(claims jwt.MapClaims, expected string) error {
	audAny, ok := claims["aud"]
	if !ok {
		return fmt.Errorf("token missing 'aud' claim (expected to contain %q)", expected)
	}

	switch v := audAny.(type) {
	case string:
		if v == expected {
			return nil
		}
		return fmt.Errorf("token aud %q does not match expected %q", v, expected)
	case []interface{}:
		for _, a := range v {
			if s, ok := a.(string); ok && s == expected {
				return nil
			}
		}
		return fmt.Errorf("token aud %v does not contain expected %q", v, expected)
	default:
		return fmt.Errorf("token aud claim has unexpected type %T", audAny)
	}
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

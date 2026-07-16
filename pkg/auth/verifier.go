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
)

// VerifierConfig holds inbound-only auth configuration for a pure resource
// server — a service that validates incoming JWTs but never mints tokens of
// its own. It carries ONLY the fields needed to verify a bearer token:
// issuer + audience (+ an optional explicit JWKSURL for tests / non-standard
// issuers).
//
// Distinct from Config, which carries BOTH the inbound-validate role and the
// outbound-mint role (client_id / client_secret / ServerURL) that
// ServiceAuthClient needs. A validate-only service (e.g. catalog-mcp) should
// use VerifierConfig; a service that also calls other services should also
// construct a ServiceAuthClient with its own Config.
//
// Environment variables (validate-only services):
//
//	env:
//	- name: LEARTECH_AUTH_ISSUER
//	  value: "<hydra-public-url>"
//	- name: LEARTECH_AUTH_AUDIENCE
//	  value: "<this-service-name>"
//
// NO LEARTECH_AUTH_SERVER_URL / CLIENT_ID / CLIENT_SECRET are required or
// consulted for the verify-only path — that's the whole point of separating
// the roles.
type VerifierConfig struct {
	// Issuer is the OAuth2 authorization-server URL that mints tokens for this
	// resource (its `iss` claim value + JWKS host). REQUIRED — empty at
	// construction returns an error. The JWKS URL is derived by resolving
	// `/.well-known/jwks.json` against this, unless JWKSURL is set explicitly.
	Issuer string `env:"LEARTECH_AUTH_ISSUER" yaml:"issuer"`
	// Audience is this resource server's audience identifier (RFC 8707).
	// Inbound tokens must carry it in their `aud` claim, otherwise they are
	// rejected. REQUIRED — empty at construction returns an error.
	Audience string `env:"LEARTECH_AUTH_AUDIENCE" yaml:"audience"`
	// JWKSURL, when set, overrides the derived Issuer + "/.well-known/jwks.json"
	// JWKS location. Useful for tests (httptest server) and for issuers that
	// advertise their JWKS at a non-standard path. Empty = derive from Issuer.
	JWKSURL string `env:"LEARTECH_AUTH_JWKS_URL" yaml:"jwksURL"`
}

// Verifier validates inbound JWTs (signature via JWKS + expiry + issuer +
// audience). It is the inbound-only counterpart of ServiceAuthClient.
//
// Fail-closed contract: constructing a Verifier with an empty Issuer or
// Audience returns an error. There is no runtime disable / noop path — a
// mis-configured resource server refuses to boot rather than silently
// accepting unvalidated tokens.
//
// Verifier NEVER mints outbound tokens: no client_id / client_secret /
// TokenSource. Services that also need to call other services construct a
// Verifier for inbound + a ServiceAuthClient for outbound; the two roles are
// separately configured and separately fail-closed.
type Verifier struct {
	cfg         VerifierConfig
	jwksKeyFunc keyfunc.Keyfunc
}

// NewVerifier constructs an inbound-only Verifier from the given
// VerifierConfig. Returns an error when Issuer or Audience is empty, when the
// derived JWKS URL is unparseable, or when the JWKS keyfunc can't be built.
//
// The ctx is currently unused (the JWKS keyfunc runs its own background
// refresh loop) but is accepted for symmetry with NewServiceClient and to
// allow future cancellation of the initial JWKS fetch.
func NewVerifier(_ context.Context, cfg VerifierConfig) (*Verifier, error) {
	if err := validateVerifierConfig(cfg); err != nil {
		return nil, err
	}

	jwksURL := cfg.JWKSURL
	if jwksURL == "" {
		issuerURL, err := url.Parse(cfg.Issuer)
		if err != nil {
			return nil, fmt.Errorf("invalid issuer URL: %w", err)
		}
		jwksURL = issuerURL.ResolveReference(&url.URL{Path: "/.well-known/jwks.json"}).String()
	}

	jwksKF, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS keyfunc from %s: %w", jwksURL, err)
	}

	return &Verifier{
		cfg:         cfg,
		jwksKeyFunc: jwksKF,
	}, nil
}

// validateVerifierConfig enforces the fail-closed construction contract for
// the inbound-only path: Issuer + Audience are both required. Returns a
// single error listing every missing field so operators see the whole gap in
// one boot log.
func validateVerifierConfig(cfg VerifierConfig) error {
	var missing []string
	if cfg.Issuer == "" {
		missing = append(missing, "LEARTECH_AUTH_ISSUER")
	}
	if cfg.Audience == "" {
		missing = append(missing, "LEARTECH_AUTH_AUDIENCE")
	}
	if len(missing) > 0 {
		return errors.New("auth: refusing to start — required verifier config missing: " +
			strings.Join(missing, ", ") +
			" (inbound token validation is mandatory; there is no runtime disable path)")
	}
	return nil
}

// Config returns the Verifier's configuration. Useful for middleware helpers
// that need to derive an RFC 9728 WWW-Authenticate hint without duplicating
// state.
func (v *Verifier) Config() VerifierConfig {
	return v.cfg
}

// DecodeToken parses a bearer token string, verifies its JWKS signature,
// checks issuer + expiry + audience, and returns the decoded TokenClaims.
// Returns an error on any validation failure — no partial acceptance.
func (v *Verifier) DecodeToken(tokenStr string) (*TokenClaims, error) {
	jwtToken, err := jwt.Parse(tokenStr, v.jwksKeyFunc.Keyfunc)
	if err != nil {
		return nil, fmt.Errorf("failed to parse/verify token: %w", err)
	}

	claims, ok := jwtToken.Claims.(jwt.MapClaims)
	if !ok || !jwtToken.Valid {
		return nil, fmt.Errorf("token is invalid")
	}

	// RFC 7519 §4.1.1 issuer binding. Enforced always — the whole point of an
	// inbound-only Verifier is that its trust root is a specific issuer. A
	// token signed by a foreign issuer with a matching JWKS key must not be
	// accepted just because the signature happens to verify.
	if err := validateIssuer(claims, v.cfg.Issuer); err != nil {
		return nil, fmt.Errorf("issuer validation failed: %w", err)
	}

	// RFC 8707 audience binding. Audience is guaranteed non-empty by
	// validateVerifierConfig at construction, so this always enforces.
	if err := validateAudience(claims, v.cfg.Audience); err != nil {
		return nil, fmt.Errorf("audience validation failed: %w", err)
	}

	return NewTokenClaimsFromMapClaims(claims)
}

// GetRequestTokenClaimsFromGinContext extracts and decodes the JWT from the
// request. Returns cached claims if a previous middleware already ran.
// Symmetric to ServiceClient.GetRequestTokenClaimsFromGinContext so a service
// that later switches from ServiceClient to Verifier for its inbound path can
// swap the constructor without touching handler code.
func (v *Verifier) GetRequestTokenClaimsFromGinContext(gc *gin.Context) (*TokenClaims, error) {
	if existing, ok := gc.Get(TokenClaimsKey); ok {
		if tc, valid := existing.(*TokenClaims); valid {
			return tc, nil
		}
	}
	token, err := getTokenFromHeader(gc.GetHeader(AuthorizationHeaderKey))
	if err != nil {
		return nil, err
	}
	return v.DecodeToken(token)
}

// validateIssuer checks the token's `iss` claim equals the expected issuer.
// A missing or type-wrong `iss` claim is a rejection.
func validateIssuer(claims jwt.MapClaims, expected string) error {
	issAny, ok := claims["iss"]
	if !ok {
		return fmt.Errorf("token missing 'iss' claim (expected %q)", expected)
	}
	iss, ok := issAny.(string)
	if !ok {
		return fmt.Errorf("token iss claim has unexpected type %T", issAny)
	}
	if iss != expected {
		return fmt.Errorf("token iss %q does not match expected %q", iss, expected)
	}
	return nil
}

// Middleware returns a gin.HandlerFunc that validates the caller's JWT (via
// the supplied Verifier) and checks required permissions. This is the
// inbound-only counterpart of ServiceClient.Middleware — services that don't
// mint outbound tokens use this so they don't need client_id / client_secret
// / ServerURL wiring.
//
// Fail-closed: no token → 401; invalid signature / wrong issuer / wrong
// audience / expired → 401; token valid but missing required perms → 403.
//
// The permission-check semantics mirror ServiceClient.Middleware exactly:
//   - requiredPerms == nil / empty → any audience-validated signed token
//     passes (matches rust + dotnet AuthLayer);
//   - otherwise the token must either carry the internal-services scope OR
//     the API scope with at least one required permission.
//
// If the caller wants to attach an RFC 9728 WWW-Authenticate hint on 401,
// pass one via NewResourceHint and construct via MiddlewareWithHint. This
// default Middleware emits no such header.
func Middleware(verifier *Verifier, requiredPerms Permissions) gin.HandlerFunc {
	return middlewareWithHint(verifier, requiredPerms, "")
}

// MiddlewareWithHint is Middleware plus an RFC 9728 §5.1 WWW-Authenticate
// resource_metadata hint attached to 401 responses. Pass the resource-metadata
// document URL (typically the value of Config.ResourceMetadataURL on the
// combined-role service).
func MiddlewareWithHint(verifier *Verifier, requiredPerms Permissions, resourceMetadataURL string) gin.HandlerFunc {
	hint := ""
	if resourceMetadataURL != "" {
		hint = `Bearer resource_metadata="` + resourceMetadataURL + `"`
	}
	return middlewareWithHint(verifier, requiredPerms, hint)
}

func middlewareWithHint(verifier *Verifier, requiredPerms Permissions, hint string) gin.HandlerFunc {
	return func(gc *gin.Context) {
		tokenClaims, err := verifier.GetRequestTokenClaimsFromGinContext(gc)
		if err != nil {
			log.Debug().Err(err).Msg("failed to decode/verify token")
			if hint != "" {
				gc.Header("WWW-Authenticate", hint)
			}
			gc.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		if isTokenAllowedAccess(requiredPerms, tokenClaims) {
			gc.Set(TokenClaimsKey, tokenClaims)
			gc.Next()
			return
		}
		log.Debug().Msg("token not authorised for required permissions")
		gc.AbortWithStatus(http.StatusForbidden)
	}
}

// isTokenAllowedAccess is the free-function twin of ServiceClient.isTokenAllowedAccess.
// Kept as a free function (not a Verifier method) because it depends only on
// the claims + required perms — never on JWKS / issuer / audience state — and
// is shared verbatim between the ServiceClient and Verifier middleware paths.
func isTokenAllowedAccess(requiredPerms Permissions, claims *TokenClaims) bool {
	if len(requiredPerms) == 0 {
		return true
	}
	return claims.Scopes.HasInternalService() ||
		(claims.Scopes.HasAPI() && claims.Permissions.IsPermitted(requiredPerms))
}

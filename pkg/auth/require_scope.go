package auth

import (
	"net/http"
	"slices"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

// RequireScope gates a route on the token carrying exactly the given
// capability scope.
//
// # Why this is not ServiceClient.RequireScopes
//
// That one hangs off ServiceClient, and every service has migrated to
// Verifier — so the shared scope gate has been unreachable from where callers
// actually stand, which is the likeliest reason it has no callers. artifact-api
// wrote its own; mcp-servers built a different shape again.
//
// The semantics differ by design, too. ServiceClient.RequireScopes is ANY-OF,
// which is right for caller-type scopes (leartechapi /
// leartechapi.internal_services): a caller is one type, so listing several
// means "any of these callers". Capability scopes are the opposite —
// artifact:write does not imply artifact:read, and artifact-api asserts both
// directions — so this takes ONE scope and is all-of by construction. A route
// needing two composes by stacking middleware, which keeps the two models
// visibly different at the call site instead of subtly different inside one
// function.
//
// # Unknown scopes cannot reach a running pod  proven-by: TestRequireScope_PanicsOnAScopeTheServiceNeverDeclared
//
// required must be in KnownScopes, and this PANICS otherwise.  proven-by: TestRequireScope_PanicsOnAScopeTheServiceNeverDeclared
//
// The panic is at construction, not per request. Route wiring returns no error, and the
// estate's posture for auth misconfiguration is already refuse-to-start: a
// missing issuer fails the pod at boot, and a route gated on a scope nothing
// can grant is the same category of mistake. Without this, a typo compiles,
// deploys, and answers 403 forever with nothing saying why — the shape the
// gateway hit with leartechapi:gateway:* being enforced and ungrantable.
//
// Checking at wiring rather than per-request also makes the check testable:
// "constructing a route with an unregistered scope panics" is three lines,
// where the per-request equivalent needs a request, a token and a registry
// state nobody sets up deliberately. That is why the gateway's request-time
// version went untested.
//
// # Status codes are fixed
//
// 401 when the token is absent, unverifiable or for the wrong audience, with
// the RFC 9728 WWW-Authenticate hint when one is configured. 403 when the token
// is valid and lacks the scope, with NO hint — the caller authenticated fine and
// re-registering cannot help.  proven-by: TestRequireScope_401CarriesTheDiscoveryHint_403DoesNot
//
// Neither is configurable: three refusal codes stay distinguishable only while
// each is fixed, and a knob lets a dashboard confuse a misconfiguration with an
// outage.  proven-by: TestRequireScope_401CarriesTheDiscoveryHint_403DoesNot
func (v *Verifier) RequireScope(required Scope, opts ...GateOption) gin.HandlerFunc {
	if required == "" {
		panic("auth: RequireScope called with an empty scope")
	}
	if !slices.Contains(v.cfg.KnownScopes, required) {
		panic("auth: RequireScope(" + string(required) + ") — not in VerifierConfig.KnownScopes " +
			formatScopes(v.cfg.KnownScopes) + ". A scope no route can name is a scope the " +
			"issuer cannot be asked to grant; declare it or fix the typo.")
	}

	cfg := gateConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	v.markGated(required)

	return func(gc *gin.Context) {
		claims, err := v.GetRequestTokenClaimsFromGinContext(gc)
		if err != nil {
			log.Debug().Err(err).Msg("failed to decode/verify token")
			if hint := v.bearerHint(); hint != "" {
				gc.Header("WWW-Authenticate", hint)
			}
			gc.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		if !slices.Contains(claims.Scopes, required) {
			log.Debug().Str("required", string(required)).Msg("token lacks the required scope")
			gc.AbortWithStatus(http.StatusForbidden)
			return
		}
		gc.Next()
	}
}

// GateOption reserves room for per-route constraints without changing any
// existing call site when one arrives.
//
// Deliberately empty today. The gateway's credential-method constraint (JWT
// vs sk-lt- virtual key) is expected to live on the SCOPE rather than here:
// keys_read is JWT-only wherever it appears, across three call sites, so a
// route-level option would be three places to remember it. That is ai-gateway's
// design note rather than a property of this package.
type GateOption func(*gateConfig)

type gateConfig struct{}

// gatedScopes records which KnownScopes some route actually gates on, so the
// unused half can be found. See UngatedScopes.
type gatedScopes struct {
	mu   sync.Mutex
	seen map[Scope]struct{}
}

func (v *Verifier) markGated(s Scope) {
	v.gated.mu.Lock()
	defer v.gated.mu.Unlock()
	if v.gated.seen == nil {
		v.gated.seen = map[Scope]struct{}{}
	}
	v.gated.seen[s] = struct{}{}
}

// UngatedScopes returns the KnownScopes that no route has gated on.
//
// KnownScopes is a second source of truth unless something ties it to the
// routes, and the two ways it can drift are not equally loud:
//
//	gated on but not declared -> RequireScope panics at wiring. LOUD.
//	declared but never gated  -> enforced by nothing. SILENT.  proven-by: TestUngatedScopes_FindsDeclaredScopesNoRouteEnforces
//
// The silent one is the enforce/publish seam inverted: a client requests the
// scope, is granted it, and gains nothing; an operator provisioning from the
// published list provisions dead scopes. The ai-gateway found exactly this in
// its own registry — leartechapi:gateway:chat and :embeddings published and
// gated by nothing — only by going looking.
//
// Call this from a test AFTER wiring every route and require it empty. That
// also makes comparing KnownScopes against the issuer's provisioned client
// meaningful: without it you have proved the issuer grants what a list says,
// and the list is the unverified part.
func (v *Verifier) UngatedScopes() Scopes {
	v.gated.mu.Lock()
	defer v.gated.mu.Unlock()
	var out Scopes
	for _, s := range v.cfg.KnownScopes {
		if _, ok := v.gated.seen[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

// bearerHint builds the RFC 9728 WWW-Authenticate hint from the verifier's own
// config. Kept in the gate rather than left to callers: ServiceClient already
// emits it (service_client.go), and discovery depends on resource_metadata being
// right.  proven-by: TestRequireScope_401CarriesTheDiscoveryHint_403DoesNot
func (v *Verifier) bearerHint() string {
	return wwwAuthenticateBearerHint(Config{
		ResourceMetadataURL: v.cfg.ResourceMetadataURL,
		ScopesSupported:     v.cfg.ScopesSupported,
	})
}

func formatScopes(ss Scopes) string {
	if len(ss) == 0 {
		return "(none declared)"
	}
	out := "["
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += string(s)
	}
	return out + "]"
}

package auth

import (
	"net/http"
	"slices"
	"strings"
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

// GatedScopes returns the KnownScopes that some route actually gates on — the
// set this service ENFORCES, as opposed to the set it declares (KnownScopes) or
// the set it advertises to clients (Config.ScopesSupported).
//
// UngatedScopes answers "what did I declare and never use". This answers "what
// do I actually require", which is the question the issuer needs answered: a
// scope this service enforces but the issuer will not grant is a route no
// caller can ever reach, and it fails as a 403 that looks exactly like a
// correctly-refused request.  proven-by: TestGatedScopes_ReportsWhatRoutesActuallyEnforce
func (v *Verifier) GatedScopes() Scopes {
	v.gated.mu.Lock()
	defer v.gated.mu.Unlock()
	var out Scopes
	for _, s := range v.cfg.KnownScopes {
		if _, ok := v.gated.seen[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// ScopeSurface is the disagreement between the three lists a resource server
// keeps about its own scopes. They are maintained in different places and
// nothing has ever compared them:
//
//	KnownScopes           declared in VerifierConfig, in code
//	routes                what RequireScope actually gates, in code
//	Config.ScopesSupported advertised at /.well-known/oauth-protected-resource,
//	                      from LEARTECH_AUTH_SCOPES_SUPPORTED — an env var
//
// Each pairing fails silently and differently, which is why they are reported
// separately rather than as one count.
type ScopeSurface struct {
	// DeclaredNotGated: in KnownScopes, gated by no route — so it is enforced
	// by nothing, and exists in provisioning while protecting nothing.
	// proven-by: TestUngatedScopes_FindsDeclaredScopesNoRouteEnforces
	DeclaredNotGated Scopes

	// PublishedNotGated: advertised in scopes_supported, gated by no route, so
	// a client reads discovery, dutifully requests the scope, and receives a
	// token whose extra scope protects nothing — over-granting caused by the
	// resource server's own metadata.
	// proven-by: TestScopeSurface_FindsAnAdvertisedScopeThatGatesNothing
	PublishedNotGated []string

	// GatedNotPublished: enforced by a route, absent from scopes_supported.
	// The worst of the three. Discovery does not list the scope, and the
	// WWW-Authenticate hint is built from ScopesSupported so it does not name
	// it either, which leaves the caller holding a 403 it cannot act on.  proven-by: TestScopeSurface_FindsAScopeEnforcedButNeverAdvertised
	// proven-by: TestScopeSurface_NoAdvertisedScopesMeansEveryGateIsUndiscoverable
	GatedNotPublished Scopes
}

// Empty reports whether all three lists agree.
// proven-by: TestScopeSurface_AgreesWhenTheThreeListsMatch
func (s ScopeSurface) Empty() bool {
	return len(s.DeclaredNotGated) == 0 &&
		len(s.PublishedNotGated) == 0 &&
		len(s.GatedNotPublished) == 0
}

// ScopeSurface compares the three lists. Call it from a test AFTER wiring every
// route — before the routes exist, everything reads as ungated and the result
// is noise rather than a finding.
//
// This is the enforcement half of the enforce/issue seam. The issuance half
// (will the issuer actually grant GatedScopes?) needs a running issuer and so
// belongs in an end2end suite; this half is a unit test, and being a unit test
// it runs on every PR rather than only where a preview exists.
func (v *Verifier) ScopeSurface() ScopeSurface {
	gated := make(map[Scope]struct{}, len(v.cfg.KnownScopes))
	for _, s := range v.GatedScopes() {
		gated[s] = struct{}{}
	}
	published := make(map[string]struct{}, len(v.cfg.ScopesSupported))
	for _, s := range v.cfg.ScopesSupported {
		published[s] = struct{}{}
	}

	out := ScopeSurface{DeclaredNotGated: v.UngatedScopes()}
	for _, s := range v.cfg.ScopesSupported {
		if _, ok := gated[Scope(s)]; !ok {
			out.PublishedNotGated = append(out.PublishedNotGated, s)
		}
	}
	for _, s := range v.GatedScopes() {
		if _, ok := published[string(s)]; !ok {
			out.GatedNotPublished = append(out.GatedNotPublished, s)
		}
	}
	return out
}

// String renders the disagreement as something a failing test can print
// verbatim, naming the consequence rather than only the difference.
func (s ScopeSurface) String() string {
	if s.Empty() {
		return "scope surface agrees: declared, gated and published are the same set"
	}
	out := ""
	if len(s.DeclaredNotGated) > 0 {
		out += "\n  declared but gated by no route " + formatScopes(s.DeclaredNotGated) +
			"\n    -> enforced by nothing; provisioned and protecting nothing"
	}
	if len(s.PublishedNotGated) > 0 {
		out += "\n  advertised in scopes_supported but gated by no route [" +
			strings.Join(s.PublishedNotGated, " ") + "]" +
			"\n    -> clients are told to request a scope that protects nothing"
	}
	if len(s.GatedNotPublished) > 0 {
		out += "\n  gated by a route but absent from scopes_supported " + formatScopes(s.GatedNotPublished) +
			"\n    -> callers cannot discover it: neither discovery nor the" +
			"\n       WWW-Authenticate hint names it, so the 403 is unactionable"
	}
	return out
}

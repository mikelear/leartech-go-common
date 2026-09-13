package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	scopeRead  Scope = "leartechapi:artifact:read"
	scopeWrite Scope = "leartechapi:artifact:write"
)

// gateFixture wires a Verifier against a real JWKS and returns a signer, so
// these tests exercise the same decode path a pod does rather than a stub.
func gateFixture(t *testing.T, cfg VerifierConfig) (*Verifier, func(scopes ...string) string) {
	t.Helper()
	srv, key, kid := verifierJWKSMock(t)
	t.Cleanup(srv.Close)

	cfg.Issuer = srv.URL
	if cfg.Audience == "" {
		cfg.Audience = "my-svc"
	}
	v, err := NewVerifier(context.Background(), cfg)
	require.NoError(t, err)

	sign := func(scopes ...string) string {
		return signVerifierToken(t, key, kid, jwt.MapClaims{
			"sub": "user-42",
			"iss": srv.URL,
			"aud": []string{cfg.Audience},
			"exp": time.Now().Add(time.Hour).Unix(),
			"scp": scopes,
		})
	}
	return v, sign
}

func callGate(t *testing.T, h gin.HandlerFunc, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/thing", h, func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/thing", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A scope the service never declared must stop the process at wiring, not
// answer 403 forever in production. This is the whole reason the check moved
// out of the request path: a typo that compiles and deploys is a route that
// can never be satisfied, and nothing says why.
func TestRequireScope_PanicsOnAScopeTheServiceNeverDeclared(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{KnownScopes: Scopes{scopeRead, scopeWrite}})

	assert.PanicsWithValue(t,
		"auth: RequireScope(leartechapi:artifact:wrtie) — not in VerifierConfig.KnownScopes "+
			"[leartechapi:artifact:read leartechapi:artifact:write]. A scope no route can name "+
			"is a scope the issuer cannot be asked to grant; declare it or fix the typo.",
		func() { v.RequireScope("leartechapi:artifact:wrtie") },
		"a misspelled scope was accepted at wiring; it would deploy and 403 forever")

	assert.Panics(t, func() { v.RequireScope("") }, "an empty scope gates nothing")
}

// The gate is all-of by construction because it takes exactly one scope.
// Capability scopes are not a hierarchy: write must not satisfy a read gate.
func TestRequireScope_OneScopeExactly_NotAHierarchy(t *testing.T) {
	v, sign := gateFixture(t, VerifierConfig{KnownScopes: Scopes{scopeRead, scopeWrite}})

	readGate := v.RequireScope(scopeRead)

	assert.Equal(t, http.StatusOK, callGate(t, readGate, sign(string(scopeRead))).Code,
		"a token carrying exactly the required scope was refused")
	assert.Equal(t, http.StatusForbidden, callGate(t, readGate, sign(string(scopeWrite))).Code,
		"a write-scoped token satisfied a read gate — capability scopes are separate "+
			"grants, not a hierarchy")
	assert.Equal(t, http.StatusForbidden, callGate(t, readGate, sign()).Code,
		"a token with scp: [] was admitted; Hydra issues exactly that when no scope is "+
			"requested, with no error, so it is the likeliest token to arrive")
	assert.Equal(t, http.StatusOK, callGate(t, readGate, sign(string(scopeWrite), string(scopeRead))).Code,
		"a token carrying the scope among others was refused")
}

// A prefix is not a match. "…:read" must not be satisfied by "…:read_all",
// which is the shape a naive strings.HasPrefix check would let through.
func TestRequireScope_PrefixIsNotAMatch(t *testing.T) {
	v, sign := gateFixture(t, VerifierConfig{KnownScopes: Scopes{scopeRead}})
	assert.Equal(t, http.StatusForbidden,
		callGate(t, v.RequireScope(scopeRead), sign("leartechapi:artifact:read_all")).Code,
		"a longer scope with the required one as a prefix was accepted")
}

// 401 means the token was rejected; 403 means it was accepted and then
// refused. The hint belongs only on the first: on a 403 the caller
// authenticated fine, and pointing them at resource metadata invites a
// re-registration that cannot help.
func TestRequireScope_401CarriesTheDiscoveryHint_403DoesNot(t *testing.T) {
	v, sign := gateFixture(t, VerifierConfig{
		KnownScopes:         Scopes{scopeRead},
		ResourceMetadataURL: "https://svc.example/.well-known/oauth-protected-resource",
		ScopesSupported:     []string{string(scopeRead)},
	})
	gate := v.RequireScope(scopeRead)

	noToken := callGate(t, gate, "")
	require.Equal(t, http.StatusUnauthorized, noToken.Code)
	assert.Contains(t, noToken.Header().Get("WWW-Authenticate"),
		`resource_metadata="https://svc.example/.well-known/oauth-protected-resource"`,
		"401 carried no RFC 9728 hint, so a client cannot discover what to ask for")

	wrongScope := callGate(t, gate, sign(string(scopeWrite)))
	require.Equal(t, http.StatusForbidden, wrongScope.Code)
	assert.Empty(t, wrongScope.Header().Get("WWW-Authenticate"),
		"403 carried a discovery hint; the caller authenticated fine and re-registering "+
			"cannot get them this scope")
}

// No hint configured must mean no header, not an empty or malformed one.
func TestRequireScope_NoHintConfigured_EmitsNoHeader(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{KnownScopes: Scopes{scopeRead}})
	w := callGate(t, v.RequireScope(scopeRead), "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Header().Get("WWW-Authenticate"))
}

// The half that fails quietly: a scope declared and provisioned that no route
// gates on. A client requests it, is granted it, and gains nothing.
func TestUngatedScopes_FindsDeclaredScopesNoRouteEnforces(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{KnownScopes: Scopes{scopeRead, scopeWrite}})

	assert.ElementsMatch(t, Scopes{scopeRead, scopeWrite}, v.UngatedScopes(),
		"before any route is wired, every declared scope is ungated")

	v.RequireScope(scopeRead)
	assert.Equal(t, Scopes{scopeWrite}, v.UngatedScopes(),
		"write is declared and no route gates on it — published, provisionable, "+
			"enforced by nothing")

	v.RequireScope(scopeWrite)
	assert.Empty(t, v.UngatedScopes(),
		"every declared scope is now gated by some route")
}

// A service with no capability scopes is legal and must not be forced to
// declare anything.
func TestUngatedScopes_EmptyVocabularyIsEmpty(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{})
	assert.Empty(t, v.UngatedScopes())
}

// Routes are wired concurrently in some services; the recorder must not race.
func TestRequireScope_ConcurrentWiringIsSafe(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{KnownScopes: Scopes{scopeRead, scopeWrite}})
	done := make(chan struct{})
	for _, s := range []Scope{scopeRead, scopeWrite} {
		go func(s Scope) { defer close(done); v.RequireScope(s) }(s)
		<-done
		done = make(chan struct{})
	}
	assert.Empty(t, v.UngatedScopes())
}

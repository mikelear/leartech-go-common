package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAudience is the inbound audience used by every hydraMock-backed test.
// Held in a constant so a signed-JWT test that needs the same value can
// reference it without diverging.
const testAudience = "leartech-go-common-test"

// Fail-closed construction: every required field must be present. Empty
// LEARTECH_AUTH_* means "auth is mis-wired" — refuse to build, never
// silently fall back to a pass-through client.
func TestNewServiceClient_FailsClosedOnMissingConfig(t *testing.T) {
	full := Config{
		ServerURL:    "https://hydra.example.com",
		ClientID:     "c",
		ClientSecret: "s",
		Audience:     "svc",
	}
	cases := []struct {
		name    string
		mutate  func(Config) Config
		wantMsg string
	}{
		{"empty ServerURL", func(c Config) Config { c.ServerURL = ""; return c }, "LEARTECH_AUTH_SERVER_URL"},
		{"empty ClientID", func(c Config) Config { c.ClientID = ""; return c }, "LEARTECH_AUTH_CLIENT_ID"},
		{"empty ClientSecret", func(c Config) Config { c.ClientSecret = ""; return c }, "LEARTECH_AUTH_CLIENT_SECRET"},
		{"empty Audience", func(c Config) Config { c.Audience = ""; return c }, "LEARTECH_AUTH_AUDIENCE"},
		{"all empty", func(_ Config) Config { return Config{} }, "LEARTECH_AUTH_SERVER_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewServiceClient(context.Background(), tc.mutate(full))
			require.Error(t, err, "missing config must error, not return a noop client")
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.Contains(t, err.Error(), "auth is mandatory")
		})
	}
}

// The old noop-on-empty-ServerURL fallback is gone: even without Required
// set, an empty ServerURL is a construction error. This test locks that in
// so a future regression that reintroduces a fail-open path is caught.
func TestNewServiceClient_NoNoopFallback(t *testing.T) {
	_, err := NewServiceClient(context.Background(), Config{})
	require.Error(t, err, "empty Config must error — there is no runtime disable path")
}

func TestNewServiceClient_InvalidServerURL(t *testing.T) {
	// url.Parse only fails on extremely malformed input — control character.
	_, err := NewServiceClient(context.Background(), Config{
		ServerURL: "http://\x7f", ClientID: "c", ClientSecret: "s", Audience: "svc",
	})
	require.Error(t, err)
}

func TestIsTokenAllowedAccess(t *testing.T) {
	c := &ServiceClient{}
	t.Run("nil requiredPerms accepts any authenticated token (mirrors Middleware(nil))", func(t *testing.T) {
		// PKCE user token from staging — scopes=[openid offline], no API scope.
		// This must pass when the handler is wrapped with `Middleware(nil)`,
		// matching rust + dotnet templates' behaviour. Strict-scope enforcement
		// at this layer was blocking valid fleet-test calls on 2026-05-18.
		claims := &TokenClaims{
			UserID: "user-test-001",
			Scopes: Scopes{Scope("openid"), Scope("offline")},
		}
		assert.True(t, c.isTokenAllowedAccess(nil, claims))
		assert.True(t, c.isTokenAllowedAccess(Permissions{}, claims))
	})
	t.Run("empty requiredPerms accepts any authenticated token", func(t *testing.T) {
		claims := &TokenClaims{UserID: "u", Scopes: Scopes{}}
		assert.True(t, c.isTokenAllowedAccess(Permissions{}, claims))
	})
	t.Run("internal-services scope grants full access", func(t *testing.T) {
		claims := &TokenClaims{Scopes: Scopes{ScopeInternalServices}}
		assert.True(t, c.isTokenAllowedAccess(Permissions{PermAdmin}, claims))
	})
	t.Run("API scope + required perm match → allowed", func(t *testing.T) {
		claims := &TokenClaims{Scopes: Scopes{ScopeAPI}, Permissions: Permissions{PermAdmin}}
		assert.True(t, c.isTokenAllowedAccess(Permissions{PermAdmin}, claims))
	})
	t.Run("API scope but missing required perm → denied", func(t *testing.T) {
		claims := &TokenClaims{Scopes: Scopes{ScopeAPI}, Permissions: Permissions{PermUser}}
		assert.False(t, c.isTokenAllowedAccess(Permissions{PermAdmin}, claims))
	})
	t.Run("no API scope, requiredPerms specified → denied", func(t *testing.T) {
		// User token without leartechapi but a specific perm required —
		// denied. The handler asked for more than "any authenticated user".
		claims := &TokenClaims{Scopes: Scopes{Scope("openid")}, Permissions: Permissions{PermAdmin}}
		assert.False(t, c.isTokenAllowedAccess(Permissions{PermAdmin}, claims))
	})
}

func TestGetTokenFromHeader(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{"empty", "", "", true},
		{"bearer lowercase", "bearer abc.def.ghi", "abc.def.ghi", false},
		{"Bearer mixed-case", "Bearer abc.def.ghi", "abc.def.ghi", false},
		{"missing scheme", "abc", "", true},
		{"wrong scheme", "Basic abc", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := getTokenFromHeader(tc.header)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestServiceClient_isTokenAllowedAccess(t *testing.T) {
	c := &ServiceClient{}

	t.Run("internal-service token has full access", func(t *testing.T) {
		claims := &TokenClaims{Scopes: Scopes{ScopeInternalServices}}
		assert.True(t, c.isTokenAllowedAccess(Permissions{PermAdmin}, claims))
	})
	t.Run("user token with matching perm", func(t *testing.T) {
		claims := &TokenClaims{Scopes: Scopes{ScopeAPI}, Permissions: Permissions{PermAdmin}}
		assert.True(t, c.isTokenAllowedAccess(Permissions{PermAdmin}, claims))
	})
	t.Run("user token without matching perm", func(t *testing.T) {
		claims := &TokenClaims{Scopes: Scopes{ScopeAPI}, Permissions: Permissions{PermUser}}
		assert.False(t, c.isTokenAllowedAccess(Permissions{PermAdmin}, claims))
	})
	t.Run("token without API scope", func(t *testing.T) {
		claims := &TokenClaims{Scopes: Scopes{}, Permissions: Permissions{PermAdmin}}
		assert.False(t, c.isTokenAllowedAccess(Permissions{PermAdmin}, claims))
	})
}

// hydraMock serves a minimal JWKS (for construction) + a token endpoint that
// records the client_credentials request's `audience` form param.
func hydraMock(t *testing.T, gotAudience *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/ready":
			w.WriteHeader(http.StatusOK)
		case "/.well-known/jwks.json":
			_, _ = w.Write([]byte(`{"keys":[]}`))
		case "/oauth2/token":
			_ = r.ParseForm()
			*gotAudience = r.Form.Get("audience")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires_in":3600}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// §A-minimal: TargetAudience → the client_credentials mint requests audience=<it>
// (so Hydra binds aud to the callee, instead of aud=[]).
func TestServiceClient_TargetAudience_RequestsAudienceParam(t *testing.T) {
	got := "SENTINEL"
	srv := hydraMock(t, &got)
	defer srv.Close()

	c, err := NewServiceClient(context.Background(), Config{
		ServerURL: srv.URL, ClientID: "cid", ClientSecret: "sec",
		Audience: testAudience, TargetAudience: "automated-agent",
	})
	require.NoError(t, err)
	_, err = c.GetAuthToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "automated-agent", got, "mint should request the configured target audience")
}

// §B: RequireScopes gates on a config-driven required scope (IsScoped), fail-closed.
func TestServiceClient_RequireScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	got := "x"
	srv := hydraMock(t, &got)
	defer srv.Close()
	c, err := NewServiceClient(context.Background(), Config{
		ServerURL: srv.URL, ClientID: "c", ClientSecret: "s", Audience: testAudience,
	})
	require.NoError(t, err)

	// Pre-cache claims so RequireScopes checks the scope without JWT validation.
	run := func(tokenScopes Scopes, required Scopes) int {
		w := httptest.NewRecorder()
		gc, _ := gin.CreateTestContext(w)
		gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
		gc.Set(TokenClaimsKey, &TokenClaims{UserID: "u", Scopes: tokenScopes})
		c.RequireScopes(required)(gc)
		return w.Code
	}

	assert.Equal(t, http.StatusOK, run(Scopes{ScopeInternalServices}, NewScopes([]string{"leartechapi.internal_services"})),
		"token with the required scope passes")
	assert.Equal(t, http.StatusForbidden, run(Scopes{ScopeAPI}, NewScopes([]string{"leartechapi.external"})),
		"token missing the required scope is 403")

	// No token at all → 401 (fail-closed).
	w := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(w)
	gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.RequireScopes(NewScopes([]string{"leartechapi.internal_services"}))(gc)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "absent token is 401")
}

func newTestClient(t *testing.T, srv *httptest.Server, cfg Config) ServiceAuthClient {
	t.Helper()
	cfg.ServerURL = srv.URL
	cfg.ClientID = "c"
	cfg.ClientSecret = "s"
	if cfg.Audience == "" {
		cfg.Audience = testAudience
	}
	c, err := NewServiceClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewServiceClient: %v", err)
	}
	return c
}

func TestServiceClient_HTTPClient(t *testing.T) {
	got := "x"
	srv := hydraMock(t, &got)
	defer srv.Close()
	c := newTestClient(t, srv, Config{})
	if c.HTTPClient() == nil {
		t.Error("HTTPClient should be non-nil")
	}
}

func TestServiceClient_GetAuthToken_And_SetAuthHeader(t *testing.T) {
	got := "x"
	srv := hydraMock(t, &got)
	defer srv.Close()
	c := newTestClient(t, srv, Config{})
	tok, err := c.GetAuthToken(context.Background())
	if err != nil || tok == nil || *tok != "tok" {
		t.Fatalf("GetAuthToken = %v (err %v); want tok", tok, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if err := c.SetAuthHeader(context.Background(), req); err != nil {
		t.Fatalf("SetAuthHeader: %v", err)
	}
	if h := req.Header.Get(AuthorizationHeaderKey); h != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", h)
	}
}

func TestServiceClient_Middleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	got := "x"
	srv := hydraMock(t, &got)
	defer srv.Close()
	c := newTestClient(t, srv, Config{})

	run := func(cl ServiceAuthClient, setup func(*gin.Context)) int {
		w := httptest.NewRecorder()
		gc, _ := gin.CreateTestContext(w)
		gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
		if setup != nil {
			setup(gc)
		}
		cl.Middleware(Permissions{"Admin"})(gc)
		return w.Code
	}
	// no token → 401
	if code := run(c, nil); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
	// pre-cached claims with Admin (API scope) → pass
	if code := run(c, func(gc *gin.Context) {
		gc.Set(TokenClaimsKey, &TokenClaims{UserID: "u", Permissions: Permissions{"Admin"}, Scopes: Scopes{ScopeAPI}})
	}); code != http.StatusOK {
		t.Errorf("admin claims = %d, want 200", code)
	}
	// pre-cached claims lacking Admin → 403
	if code := run(c, func(gc *gin.Context) {
		gc.Set(TokenClaimsKey, &TokenClaims{UserID: "u", Permissions: Permissions{"User"}, Scopes: Scopes{ScopeAPI}})
	}); code != http.StatusForbidden {
		t.Errorf("non-admin claims = %d, want 403", code)
	}
}

func TestServiceClient_Middleware_BogusToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	got := "x"
	srv := hydraMock(t, &got)
	defer srv.Close()
	c := newTestClient(t, srv, Config{})
	w := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(w)
	gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	gc.Request.Header.Set(AuthorizationHeaderKey, "Bearer not-a-real-jwt") // decodeToken must reject
	c.Middleware(nil)(gc)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bogus token = %d, want 401", w.Code)
	}
}

func TestServiceClient_Ping(t *testing.T) {
	got := "x"
	srv := hydraMock(t, &got)
	defer srv.Close()
	if err := newTestClient(t, srv, Config{}).Ping(context.Background()); err != nil {
		t.Errorf("Ping (health 200) = %v, want nil", err)
	}
	bad, err := NewServiceClient(context.Background(), Config{
		ServerURL: "http://127.0.0.1:0", ClientID: "c", ClientSecret: "s", Audience: testAudience,
	})
	if err != nil {
		t.Fatalf("NewServiceClient: %v", err)
	}
	if err := bad.Ping(context.Background()); err == nil {
		t.Error("Ping (unreachable) should error")
	}
}

func TestNewScopes(t *testing.T) {
	got := NewScopes([]string{"leartechapi.internal_services", "  spaced  ", ""})
	assert.Equal(t, Scopes{"leartechapi.internal_services", "spaced"}, got, "trims + drops blanks")
	assert.Empty(t, NewScopes(nil))
}

// No TargetAudience → no audience param (unchanged legacy behaviour). Note
// that the service's OWN Audience (inbound) is still mandatory; only the
// OUTBOUND TargetAudience is optional.
func TestServiceClient_NoTargetAudience_OmitsAudienceParam(t *testing.T) {
	got := "SENTINEL"
	srv := hydraMock(t, &got)
	defer srv.Close()

	c, err := NewServiceClient(context.Background(), Config{
		ServerURL: srv.URL, ClientID: "cid", ClientSecret: "sec", Audience: testAudience,
	})
	require.NoError(t, err)
	_, err = c.GetAuthToken(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "no TargetAudience → no audience param on the mint")
}

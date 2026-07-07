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

func TestNewServiceClient_EmptyServerURL_ReturnsNoop(t *testing.T) {
	c, err := NewServiceClient(context.Background(), Config{})
	require.NoError(t, err)
	assert.True(t, c.IsDisabled())

	tok, err := c.GetAuthToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "", *tok)

	req, _ := http.NewRequest(http.MethodGet, "http://x/y", nil)
	require.NoError(t, c.SetAuthHeader(context.Background(), req))
	assert.Empty(t, req.Header.Get("Authorization"))

	require.NoError(t, c.Ping(context.Background()))
	assert.NotNil(t, c.HTTPClient())
}

func TestNoopMiddleware_AlwaysPasses(t *testing.T) {
	c, err := NewServiceClient(context.Background(), Config{})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", c.Middleware(Permissions{PermAdmin}), func(gc *gin.Context) {
		gc.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "noop middleware must permit all requests")
}

func TestNoopGetRequestTokenClaimsFromGinContext(t *testing.T) {
	c, err := NewServiceClient(context.Background(), Config{})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	claims, err := c.GetRequestTokenClaimsFromGinContext(gc)
	require.NoError(t, err)
	assert.Equal(t, "dev-user", claims.UserID)
	assert.Contains(t, claims.Permissions, PermAdmin)
}

func TestNewServiceClient_InvalidServerURL(t *testing.T) {
	// url.Parse only fails on extremely malformed input — control character.
	_, err := NewServiceClient(context.Background(), Config{ServerURL: "http://\x7f"})
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
		ServerURL: srv.URL, ClientID: "cid", ClientSecret: "sec", TargetAudience: "automated-agent",
	})
	require.NoError(t, err)
	_, err = c.GetAuthToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "automated-agent", got, "mint should request the configured target audience")
}

// No TargetAudience → no audience param (unchanged legacy behaviour).
func TestServiceClient_NoTargetAudience_OmitsAudienceParam(t *testing.T) {
	got := "SENTINEL"
	srv := hydraMock(t, &got)
	defer srv.Close()

	c, err := NewServiceClient(context.Background(), Config{
		ServerURL: srv.URL, ClientID: "cid", ClientSecret: "sec",
	})
	require.NoError(t, err)
	_, err = c.GetAuthToken(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "no TargetAudience → no audience param on the mint")
}

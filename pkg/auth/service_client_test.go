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

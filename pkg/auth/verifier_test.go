package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// verifierJWKSMock stands in for a Hydra JWKS endpoint. It generates one RSA
// keypair, serves it at /.well-known/jwks.json (and optionally at
// /custom/jwks.json when using an explicit JWKSURL override), and returns the
// private key so the test can sign tokens the Verifier will validate.
//
// This does NOT expose an /oauth2/token endpoint — the whole point of the
// Verifier is that it validates without ever minting. Its absence is proof
// the Verifier doesn't need it.
func verifierJWKSMock(t *testing.T) (*httptest.Server, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err, "rsa key gen")
	kid := "verifier-test-kid"
	pub := &key.PublicKey
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	jwks := fmt.Sprintf(`{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q}]}`, kid, n, e)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json", "/custom/jwks.json":
			_, _ = w.Write([]byte(jwks))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, key, kid
}

// signVerifierToken mints a signed JWT for use against a Verifier.
func signVerifierToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	require.NoError(t, err, "sign token")
	return s
}

// TestNewVerifier_FailsClosedOnMissingConfig locks in the fail-closed
// construction contract: missing Issuer or Audience is a boot-time error,
// never a silent fall-through to a pass-through Verifier.
func TestNewVerifier_FailsClosedOnMissingConfig(t *testing.T) {
	// The construction succeeds only when Issuer + Audience are both set
	// (JWKS URL is derived). Anything less must error.
	cases := []struct {
		name    string
		cfg     VerifierConfig
		wantMsg string
	}{
		{"empty Issuer", VerifierConfig{Audience: "svc"}, "LEARTECH_AUTH_ISSUER"},
		{"empty Audience", VerifierConfig{Issuer: "https://hydra.example.com"}, "LEARTECH_AUTH_AUDIENCE"},
		{"both empty", VerifierConfig{}, "LEARTECH_AUTH_ISSUER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewVerifier(context.Background(), tc.cfg)
			require.Error(t, err, "missing config must error, not return a noop verifier")
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.Contains(t, err.Error(), "inbound token validation is mandatory")
		})
	}
}

// TestNewVerifier_NoClientCredsRequired proves the whole point of separating
// the inbound-only role: a validate-only resource server can construct a
// Verifier with ONLY issuer + audience — no LEARTECH_AUTH_SERVER_URL,
// CLIENT_ID or CLIENT_SECRET. If NewVerifier ever starts requiring them,
// this test breaks and the regression is caught immediately.
func TestNewVerifier_NoClientCredsRequired(t *testing.T) {
	srv, _, _ := verifierJWKSMock(t)
	defer srv.Close()

	// Deliberately construct with the bare minimum. Zero client creds.
	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer:   srv.URL,
		Audience: "catalog-mcp",
	})
	require.NoError(t, err, "verifier must construct with issuer+audience only — no client creds")
	require.NotNil(t, v)
	// The stored config must not have accreted the client-cred fields.
	got := v.Config()
	assert.Equal(t, srv.URL, got.Issuer)
	assert.Equal(t, "catalog-mcp", got.Audience)
}

// TestNewVerifier_InvalidIssuerURL: bad control-char URL must error.
func TestNewVerifier_InvalidIssuerURL(t *testing.T) {
	_, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: "http://\x7f", Audience: "svc",
	})
	require.Error(t, err)
}

// TestNewVerifier_ExplicitJWKSURL: JWKSURL override lets the caller point at
// a non-standard JWKS location (useful for mocks + issuers that don't
// advertise /.well-known/jwks.json).
func TestNewVerifier_ExplicitJWKSURL(t *testing.T) {
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer:   srv.URL,
		Audience: "svc",
		JWKSURL:  srv.URL + "/custom/jwks.json",
	})
	require.NoError(t, err)

	// Prove the override actually got wired in: sign a valid token, decode.
	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"iss": srv.URL,
		"aud": []string{"svc"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	claims, err := v.DecodeToken(token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.UserID)
}

// TestVerifier_DecodeToken_Success: signature+issuer+audience+exp all match →
// claims returned (including ext identity fields).
func TestVerifier_DecodeToken_Success(t *testing.T) {
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-42",
		"iss": srv.URL,
		"aud": []string{"my-svc"},
		"exp": time.Now().Add(time.Hour).Unix(),
		"scp": []string{"leartechapi"},
		"ext": map[string]any{
			"Permissions": []string{"Admin"},
			"tenant_id":   "t-1",
			"user_role":   "admin",
			"external_id": "ext-1",
		},
	})
	claims, err := v.DecodeToken(token)
	require.NoError(t, err)
	assert.Equal(t, "user-42", claims.UserID)
	assert.Equal(t, "t-1", claims.TenantID)
	assert.Equal(t, "admin", claims.UserRole)
	assert.Equal(t, "ext-1", claims.ExternalID)
	assert.Contains(t, claims.Permissions, PermAdmin)
	assert.Contains(t, claims.Scopes, ScopeAPI)
}

// TestVerifier_DecodeToken_WrongAudience: RFC 8707 binding enforced.
func TestVerifier_DecodeToken_WrongAudience(t *testing.T) {
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"iss": srv.URL,
		"aud": []string{"someone-else"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err = v.DecodeToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audience")
}

// TestVerifier_DecodeToken_WrongIssuer: iss binding enforced independently of
// signature. Even a token whose sig verifies against our JWKS is rejected
// when the iss claim points elsewhere.
func TestVerifier_DecodeToken_WrongIssuer(t *testing.T) {
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"iss": "https://evil.example.com",
		"aud": []string{"my-svc"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err = v.DecodeToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issuer")
}

// TestVerifier_DecodeToken_MissingIssuer: `iss` claim absent → reject.
func TestVerifier_DecodeToken_MissingIssuer(t *testing.T) {
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	// No `iss` claim at all.
	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"aud": []string{"my-svc"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err = v.DecodeToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing 'iss'")
}

// TestVerifier_DecodeToken_BadSignature: token signed by an unrelated key is
// rejected. Guards against a foreign issuer's token being accepted just
// because its structure looks right.
func TestVerifier_DecodeToken_BadSignature(t *testing.T) {
	srv, _, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	// Sign with a DIFFERENT key so the signature verification fails.
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	token := signVerifierToken(t, otherKey, kid, jwt.MapClaims{
		"sub": "user-1",
		"iss": srv.URL,
		"aud": []string{"my-svc"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err = v.DecodeToken(token)
	require.Error(t, err)
}

// TestVerifier_DecodeToken_Expired: exp in the past → reject.
func TestVerifier_DecodeToken_Expired(t *testing.T) {
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"iss": srv.URL,
		"aud": []string{"my-svc"},
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	_, err = v.DecodeToken(token)
	require.Error(t, err)
}

// TestVerifier_DecodeToken_MalformedTokenString: not a JWT → reject.
func TestVerifier_DecodeToken_MalformedTokenString(t *testing.T) {
	srv, _, _ := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	_, err = v.DecodeToken("not-a-real-jwt")
	require.Error(t, err)
}

// TestValidateIssuer covers the raw helper with all input shapes; the
// integration path is exercised by TestVerifier_DecodeToken_*.
func TestValidateIssuer(t *testing.T) {
	t.Run("match", func(t *testing.T) {
		err := validateIssuer(jwt.MapClaims{"iss": "https://hydra.example.com"}, "https://hydra.example.com")
		assert.NoError(t, err)
	})
	t.Run("mismatch", func(t *testing.T) {
		err := validateIssuer(jwt.MapClaims{"iss": "https://other.example.com"}, "https://hydra.example.com")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "does not match")
	})
	t.Run("missing", func(t *testing.T) {
		err := validateIssuer(jwt.MapClaims{"sub": "user-1"}, "https://hydra.example.com")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing 'iss'")
	})
	t.Run("wrong type", func(t *testing.T) {
		err := validateIssuer(jwt.MapClaims{"iss": 42}, "https://hydra.example.com")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected type")
	})
}

// TestMiddleware_NoToken: absent Authorization → 401.
func TestMiddleware_NoToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _, _ := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(w)
	gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	Middleware(v, nil)(gc)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "absent bearer token is 401")
}

// TestMiddleware_InvalidToken: malformed bearer → 401.
func TestMiddleware_InvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _, _ := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(w)
	gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	gc.Request.Header.Set(AuthorizationHeaderKey, "Bearer not-a-real-jwt")
	Middleware(v, nil)(gc)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "malformed bearer is 401")
}

// TestMiddleware_ValidToken_PassesHandler: with a valid token the middleware
// stores claims on the gin context, calls Next(), and the handler responds
// 200 — the round-trip a resource server actually cares about.
func TestMiddleware_ValidToken_PassesHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	// Sign a valid token with the internal-services scope so the perm gate
	// (below) passes without needing ext.Permissions.
	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"iss": srv.URL,
		"aud": []string{"my-svc"},
		"scp": []string{"leartechapi.internal_services"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	router := gin.New()
	router.GET("/x", Middleware(v, Permissions{PermAdmin}), func(gc *gin.Context) {
		// The handler must be reachable AND see the decoded claims.
		claimsAny, ok := gc.Get(TokenClaimsKey)
		require.True(t, ok, "middleware must set claims on the context")
		claims, ok := claimsAny.(*TokenClaims)
		require.True(t, ok)
		assert.Equal(t, "user-1", claims.UserID)
		gc.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(AuthorizationHeaderKey, "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestMiddleware_ValidToken_NilRequiredPerms: any authenticated token passes
// when requiredPerms is nil — matches ServiceClient.Middleware(nil) behaviour
// so a PKCE user token with only openid+offline scopes is accepted.
func TestMiddleware_ValidToken_NilRequiredPerms(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"iss": srv.URL,
		"aud": []string{"my-svc"},
		"scp": []string{"openid", "offline"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(w)
	gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	gc.Request.Header.Set(AuthorizationHeaderKey, "Bearer "+token)
	Middleware(v, nil)(gc)

	assert.Equal(t, http.StatusOK, w.Code, "any-authenticated-user path passes")
}

// TestMiddleware_MissingPerm403: token is valid but lacks the required perm →
// 403 (not 401 — auth succeeded, authz failed).
func TestMiddleware_MissingPerm403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, key, kid := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	// API scope + User perm — required Admin is missing.
	token := signVerifierToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"iss": srv.URL,
		"aud": []string{"my-svc"},
		"scp": []string{"leartechapi"},
		"exp": time.Now().Add(time.Hour).Unix(),
		"ext": map[string]any{"Permissions": []string{"User"}},
	})

	w := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(w)
	gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	gc.Request.Header.Set(AuthorizationHeaderKey, "Bearer "+token)
	Middleware(v, Permissions{PermAdmin})(gc)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestMiddlewareWithHint_SetsWWWAuthenticateOn401: RFC 9728 §5.1 hint
// attached only when configured, only on 401.
func TestMiddlewareWithHint_SetsWWWAuthenticateOn401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _, _ := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	const meta = "https://api.example.com/.well-known/oauth-protected-resource"

	// 401 path: hint is emitted.
	w := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(w)
	gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	MiddlewareWithHint(v, nil, meta)(gc)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Bearer resource_metadata="`+meta+`"`, w.Header().Get("WWW-Authenticate"))

	// No metadata URL configured → no header, existing legacy behaviour.
	w2 := httptest.NewRecorder()
	gc2, _ := gin.CreateTestContext(w2)
	gc2.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	MiddlewareWithHint(v, nil, "")(gc2)
	assert.Equal(t, http.StatusUnauthorized, w2.Code)
	assert.Empty(t, w2.Header().Get("WWW-Authenticate"), "no hint when metadata URL empty")
}

// TestVerifier_GetRequestTokenClaimsFromGinContext_UsesCached: if an earlier
// middleware already populated TokenClaimsKey, the Verifier reads it directly
// without re-parsing — mirrors ServiceClient's behaviour so ordering chains
// work identically.
func TestVerifier_GetRequestTokenClaimsFromGinContext_UsesCached(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _, _ := verifierJWKSMock(t)
	defer srv.Close()

	v, err := NewVerifier(context.Background(), VerifierConfig{
		Issuer: srv.URL, Audience: "my-svc",
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(w)
	gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	// Deliberately provide NO bearer header — if the Verifier tries to
	// re-parse, it errors. Cached path must skip that.
	pre := &TokenClaims{UserID: "user-cached", Scopes: Scopes{ScopeAPI}}
	gc.Set(TokenClaimsKey, pre)

	got, err := v.GetRequestTokenClaimsFromGinContext(gc)
	require.NoError(t, err)
	assert.Same(t, pre, got, "cached claims returned as-is")
}

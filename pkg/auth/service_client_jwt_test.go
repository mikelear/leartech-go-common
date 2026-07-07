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
)

// signedJWKSMock serves a JWKS for a freshly-generated RSA key (+ /health/ready)
// and returns the key + kid so a test can sign tokens the client will validate
// against that JWKS — the real inbound-validation path the empty-JWKS mock can't
// reach.
func signedJWKSMock(t *testing.T) (*httptest.Server, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	kid := "test-kid"
	pub := &key.PublicKey
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	jwks := fmt.Sprintf(`{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q}]}`, kid, n, e)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			_, _ = w.Write([]byte(jwks))
		case "/health/ready":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, key, kid
}

func signToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

// TestServiceClient_Middleware_RealTokenValidation exercises the security-
// critical inbound path end to end: fetch JWKS, verify signature, validate the
// audience (RFC 8707), extract claims (incl. ext identity), enforce permissions.
func TestServiceClient_Middleware_RealTokenValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, key, kid := signedJWKSMock(t)
	defer srv.Close()
	c, err := NewServiceClient(context.Background(), Config{
		ServerURL: srv.URL, ClientID: "c", ClientSecret: "s", Audience: "my-svc",
	})
	if err != nil {
		t.Fatalf("NewServiceClient: %v", err)
	}

	run := func(token string) (int, *TokenClaims) {
		w := httptest.NewRecorder()
		gc, _ := gin.CreateTestContext(w)
		gc.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
		if token != "" {
			gc.Request.Header.Set(AuthorizationHeaderKey, "Bearer "+token)
		}
		c.Middleware(Permissions{"Admin"})(gc)
		var claims *TokenClaims
		if v, ok := gc.Get(TokenClaimsKey); ok {
			claims, _ = v.(*TokenClaims)
		}
		return w.Code, claims
	}

	// Valid: right audience, Admin permission + API scope → pass + claims extracted.
	code, claims := run(signToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1",
		"scp": []string{"leartechapi"},
		"aud": []string{"my-svc"},
		"exp": time.Now().Add(time.Hour).Unix(),
		"ext": map[string]any{"Permissions": []string{"Admin"}, "tenant_id": "t1", "user_role": "admin"},
	}))
	if code != http.StatusOK {
		t.Fatalf("valid token = %d, want 200", code)
	}
	if claims == nil || claims.UserID != "user-1" || claims.TenantID != "t1" || claims.UserRole != "admin" {
		t.Errorf("claims not extracted correctly: %+v", claims)
	}

	// Wrong audience → 401 (RFC 8707 binding enforced).
	if code, _ := run(signToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1", "scp": []string{"leartechapi"}, "aud": []string{"someone-else"},
		"exp": time.Now().Add(time.Hour).Unix(), "ext": map[string]any{"Permissions": []string{"Admin"}},
	})); code != http.StatusUnauthorized {
		t.Errorf("wrong audience = %d, want 401", code)
	}

	// Expired → 401.
	if code, _ := run(signToken(t, key, kid, jwt.MapClaims{
		"sub": "user-1", "aud": []string{"my-svc"}, "exp": time.Now().Add(-time.Hour).Unix(),
		"ext": map[string]any{"Permissions": []string{"Admin"}},
	})); code != http.StatusUnauthorized {
		t.Errorf("expired token = %d, want 401", code)
	}
}

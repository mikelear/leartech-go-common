package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestResourceMetadataHandler covers the RFC 9728 §3 metadata document: served
// when configured, 404 when discovery is off (either required field empty).
func TestResourceMetadataHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name     string
		cfg      Config
		wantCode int
	}{
		{"happy", Config{Resource: "https://api.example.com", AuthorizationServers: []string{"https://hydra.example.com"}}, http.StatusOK},
		{"multiple-as", Config{Resource: "https://api.example.com", AuthorizationServers: []string{"https://a.example.com", "https://b.example.com"}}, http.StatusOK},
		{"off-no-resource", Config{AuthorizationServers: []string{"https://hydra.example.com"}}, http.StatusNotFound},
		{"off-no-as", Config{Resource: "https://api.example.com"}, http.StatusNotFound},
		{"off-empty", Config{}, http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.GET(ProtectedResourceMetadataPath, ResourceMetadataHandler(tc.cfg))
			req := httptest.NewRequest(http.MethodGet, ProtectedResourceMetadataPath, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", w.Code, tc.wantCode)
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			// RFC 9728 mandates lowercase snake_case wire names.
			var raw map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatalf("body not JSON: %v", err)
			}
			if _, ok := raw["resource"]; !ok {
				t.Error("missing `resource`")
			}
			if _, ok := raw["authorization_servers"]; !ok {
				t.Error("missing `authorization_servers`")
			}
		})
	}
}

// TestNewProtectedResourceMetadata_DefensiveCopy: mutating the returned struct
// must not reach back into the source config's slice.
func TestNewProtectedResourceMetadata_DefensiveCopy(t *testing.T) {
	cfg := Config{Resource: "https://api.example.com", AuthorizationServers: []string{"https://hydra.example.com"}}
	md := NewProtectedResourceMetadata(cfg)
	if md == nil {
		t.Fatal("expected metadata, got nil")
	}
	md.AuthorizationServers[0] = "MUTATED"
	if cfg.AuthorizationServers[0] != "https://hydra.example.com" {
		t.Error("mutation bled back into source config")
	}
}

// TestWWWAuthenticateBearerHint: emitted only when ResourceMetadataURL is set
// (RFC 9728 §5.1); empty otherwise so legacy 401s are byte-for-byte unchanged.
func TestWWWAuthenticateBearerHint(t *testing.T) {
	const url = "https://api.example.com/.well-known/oauth-protected-resource"
	if got := wwwAuthenticateBearerHint(Config{ResourceMetadataURL: url}); got != `Bearer resource_metadata="`+url+`"` {
		t.Errorf("configured hint = %q", got)
	}
	if got := wwwAuthenticateBearerHint(Config{}); got != "" {
		t.Errorf("unconfigured hint = %q, want empty", got)
	}
}

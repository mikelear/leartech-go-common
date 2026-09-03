package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// ── scopes_supported (RFC 9728 §3.1) ────────────────────────────────
//
// WHY THESE EXIST. Publishing the scopes is what lets a conforming client
// request the right ones. Without it, clients fall back to the authorisation
// server's scopes_supported — Hydra advertises only openid/offline/
// offline_access and knows nothing of custom scopes — so a native MCP client
// registered with three OIDC scopes and was then refused 403 by every
// leartechapi:*-gated route. Enforced but unpublished is the defect.

func TestResourceMetadata_PublishesScopesSupported(t *testing.T) {
	m := NewProtectedResourceMetadata(Config{
		Resource:             "https://mcp.example",
		AuthorizationServers: []string{"https://hydra.example"},
		ScopesSupported:      []string{"leartechapi:artifact:read", "leartechapi:mcp:read"},
	})
	if m == nil {
		t.Fatal("metadata is nil for a fully configured resource")
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	list, ok := got["scopes_supported"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("scopes_supported = %v, want the two configured scopes", got["scopes_supported"])
	}
}

// Omitted entirely when unset: a resource that has no custom scopes must not
// publish an empty list, which a client could read as "no scopes needed".
func TestResourceMetadata_OmitsScopesWhenUnset(t *testing.T) {
	m := NewProtectedResourceMetadata(Config{
		Resource:             "https://mcp.example",
		AuthorizationServers: []string{"https://hydra.example"},
	})
	b, _ := json.Marshal(m)
	if strings.Contains(string(b), "scopes_supported") {
		t.Errorf("document carries scopes_supported when none configured: %s", b)
	}
}

// The defensive copy must cover the new slice too, or a caller mutating its
// config after startup silently rewrites what the server advertises.
func TestResourceMetadata_ScopesAreDefensivelyCopied(t *testing.T) {
	src := []string{"leartechapi:artifact:read"}
	m := NewProtectedResourceMetadata(Config{
		Resource:             "https://mcp.example",
		AuthorizationServers: []string{"https://hydra.example"},
		ScopesSupported:      src,
	})
	src[0] = "mutated"
	if m.ScopesSupported[0] != "leartechapi:artifact:read" {
		t.Errorf("ScopesSupported aliases the caller's slice: %v", m.ScopesSupported)
	}
}

// The 401 challenge must carry the same scopes as the document. A client that
// never fetches the metadata still learns what to ask for from the response.
func TestWWWAuthenticateHint_CarriesScope(t *testing.T) {
	got := wwwAuthenticateBearerHint(Config{
		ResourceMetadataURL: "https://mcp.example/.well-known/oauth-protected-resource",
		ScopesSupported:     []string{"leartechapi:artifact:read", "leartechapi:artifact:write"},
	})
	if !strings.Contains(got, `scope="leartechapi:artifact:read leartechapi:artifact:write"`) {
		t.Errorf("hint = %q, want a space-delimited scope= parameter (RFC 6750 §3)", got)
	}
	if !strings.Contains(got, "resource_metadata=") {
		t.Errorf("hint = %q, dropped resource_metadata", got)
	}
}

// Same config drives both surfaces, so they cannot disagree. Pinned, because a
// future refactor that builds them from separate inputs would reintroduce
// exactly the drift this change exists to remove.
func TestHintAndDocumentAdvertiseTheSameScopes(t *testing.T) {
	cfg := Config{
		Resource:             "https://mcp.example",
		AuthorizationServers: []string{"https://hydra.example"},
		ResourceMetadataURL:  "https://mcp.example/.well-known/oauth-protected-resource",
		ScopesSupported:      []string{"leartechapi:mcp:read", "leartechapi:mcp:write"},
	}
	doc := NewProtectedResourceMetadata(cfg)
	hint := wwwAuthenticateBearerHint(cfg)
	for _, s := range doc.ScopesSupported {
		if !strings.Contains(hint, s) {
			t.Errorf("document advertises %q but the 401 hint does not: %q", s, hint)
		}
	}
}

// No hint at all without a metadata URL, scopes or not — unchanged opt-in
// behaviour for consumers that have not wired discovery.
func TestWWWAuthenticateHint_EmptyWithoutMetadataURL(t *testing.T) {
	if got := wwwAuthenticateBearerHint(Config{
		ScopesSupported: []string{"leartechapi:mcp:read"},
	}); got != "" {
		t.Errorf("hint = %q, want empty when ResourceMetadataURL is unset", got)
	}
}

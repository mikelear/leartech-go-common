package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ProtectedResourceMetadataPath is the well-known path defined by RFC 9728
// for the protected-resource-metadata document.
const ProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource"

// ProtectedResourceMetadata represents an RFC 9728 §3.1 OAuth 2.0 Protected
// Resource Metadata document. Only the minimum set of fields needed for an MCP
// host / OAuth resource server to advertise its authorisation servers is
// populated. Additional optional members from RFC 9728 may be added without
// breaking consumers (JSON marshalling ignores unknown fields on consumers'
// side too).
type ProtectedResourceMetadata struct {
	// Resource is the canonical URI of the resource server. REQUIRED.
	Resource string `json:"resource"`
	// AuthorizationServers is the list of issuer URLs that can issue tokens
	// for this resource. REQUIRED for the helper to be useful.
	AuthorizationServers []string `json:"authorization_servers"`
	// ScopesSupported lists the scopes a client must request in order to use
	// this resource (RFC 9728 §3.1 `scopes_supported`). OPTIONAL in the RFC,
	// omitted when empty — but omitting it has a concrete cost.
	//
	// WHY IT MATTERS. A well-behaved client requests exactly what discovery
	// advertises. With this absent, clients fall back to the AUTHORISATION
	// SERVER's `scopes_supported`, and Hydra advertises only
	// openid/offline/offline_access — it knows nothing of custom scopes. A
	// native MCP client therefore registered with three OIDC scopes and was
	// refused 403 by every leartechapi:*-gated route. The scopes were
	// enforced but never published, leaving callers no way to discover them
	// short of reading Go source or inferring from a 403.
	ScopesSupported []string `json:"scopes_supported,omitempty"`
}

// NewProtectedResourceMetadata builds a metadata document from the supplied
// config. Returns nil if discovery is not configured (Resource or
// AuthorizationServers empty) — callers MUST treat that as "discovery off".
func NewProtectedResourceMetadata(cfg Config) *ProtectedResourceMetadata {
	if cfg.Resource == "" || len(cfg.AuthorizationServers) == 0 {
		return nil
	}
	// Defensive copy of the slice so callers can't mutate the source config
	// through the returned struct.
	servers := make([]string, len(cfg.AuthorizationServers))
	copy(servers, cfg.AuthorizationServers)
	scopes := make([]string, len(cfg.ScopesSupported))
	copy(scopes, cfg.ScopesSupported)
	return &ProtectedResourceMetadata{
		Resource:             cfg.Resource,
		AuthorizationServers: servers,
		ScopesSupported:      scopes,
	}
}

// ResourceMetadataHandler returns a gin.HandlerFunc that serves the RFC 9728
// protected-resource-metadata document at /.well-known/oauth-protected-resource.
//
// When discovery is not configured (Resource empty or AuthorizationServers
// empty), the handler responds with 404 Not Found — this matches the
// "feature off by default" contract: a consumer that wires this handler
// without populating the config gets a quiet 404, not an empty/half-valid
// document.
//
// Consumers register the handler explicitly; the package does NOT auto-route.
// Existing consumers that don't opt in are unaffected.
func ResourceMetadataHandler(cfg Config) gin.HandlerFunc {
	metadata := NewProtectedResourceMetadata(cfg)
	return func(gc *gin.Context) {
		if metadata == nil {
			gc.AbortWithStatus(http.StatusNotFound)
			return
		}
		gc.JSON(http.StatusOK, metadata)
	}
}

// wwwAuthenticateBearerHint returns the value for the WWW-Authenticate header
// hint per RFC 9728 §5.1, or "" when no resource_metadata URL is configured.
// Empty string means "do not emit the header" — the existing 401 behaviour
// is preserved for consumers that don't opt in.
// It also carries `scope=` (RFC 6750 §3) when ScopesSupported is configured, so
// a client that gets a 401 learns what to ask for from the response itself
// rather than having to fetch and parse the metadata document. Belt and braces
// with the document: the two are built from the same config, so they cannot
// disagree.
func wwwAuthenticateBearerHint(cfg Config) string {
	if cfg.ResourceMetadataURL == "" {
		return ""
	}
	hint := `Bearer resource_metadata="` + cfg.ResourceMetadataURL + `"`
	if len(cfg.ScopesSupported) > 0 {
		hint += `, scope="` + strings.Join(cfg.ScopesSupported, " ") + `"`
	}
	return hint
}

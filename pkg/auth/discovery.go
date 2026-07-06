package auth

import (
	"net/http"

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
	return &ProtectedResourceMetadata{
		Resource:             cfg.Resource,
		AuthorizationServers: servers,
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
func wwwAuthenticateBearerHint(cfg Config) string {
	if cfg.ResourceMetadataURL == "" {
		return ""
	}
	return `Bearer resource_metadata="` + cfg.ResourceMetadataURL + `"`
}

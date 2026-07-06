package auth

// Config holds authentication configuration for connecting to Hydra.
//
// Environment variables (set via Kubernetes secrets from ExternalSecrets):
//
//	env:
//	- name: LEARTECH_AUTH_SERVER_URL
//	  valueFrom:
//	    secretKeyRef:
//	      key: BASE_URL
//	      name: backend-service-oauth
//	- name: LEARTECH_AUTH_CLIENT_ID
//	  valueFrom:
//	    secretKeyRef:
//	      key: CLIENT_ID
//	      name: backend-service-oauth
//	- name: LEARTECH_AUTH_CLIENT_SECRET
//	  valueFrom:
//	    secretKeyRef:
//	      key: CLIENT_SECRET
//	      name: backend-service-oauth
type Config struct {
	// ServerURL is the Hydra public URL for the cluster (set via LEARTECH_AUTH_SERVER_URL).
	ServerURL string `env:"LEARTECH_AUTH_SERVER_URL" yaml:"serverURL"`
	// ClientID for the OAuth2 client_credentials flow
	ClientID string `env:"LEARTECH_AUTH_CLIENT_ID" yaml:"clientID"`
	// ClientSecret for the OAuth2 client_credentials flow
	ClientSecret string `env:"LEARTECH_AUTH_CLIENT_SECRET" yaml:"clientSecret"`
	// Audience is this service's own audience identifier (per RFC 8707).
	// Inbound JWT validation requires the token's `aud` claim to contain
	// this value — rejecting tokens issued for other services even when
	// they're signed by the same Hydra. Mirrors rust + dotnet templates'
	// audience-binding behaviour.
	//
	// Empty string disables aud validation (legacy lenient behaviour;
	// allowed for backwards-compat during rollout, but production
	// deploys should always set this). When unset, Middleware logs a
	// WARN on every request so the gap is visible.
	Audience string `env:"LEARTECH_AUTH_AUDIENCE" yaml:"audience"`
	// DisableMiddleware stops endpoint auth checks (local dev only, never in prod)
	DisableMiddleware bool `yaml:"disableMiddleware"`

	// --- RFC 9728 OAuth 2.0 Protected Resource Metadata (opt-in) ---
	// Populated only by resource servers (e.g. the public MCP host) that
	// advertise their authorisation server(s) to external clients. All three
	// empty = discovery off: ResourceMetadataHandler 404s and Middleware's
	// 401s carry no WWW-Authenticate hint (unchanged legacy behaviour).

	// Resource is this resource server's canonical URI (RFC 9728 §3.1 `resource`).
	Resource string `env:"LEARTECH_AUTH_RESOURCE" yaml:"resource"`
	// AuthorizationServers lists the issuer URLs that mint tokens for this
	// resource (RFC 9728 §3.1 `authorization_servers`).
	AuthorizationServers []string `env:"LEARTECH_AUTH_AUTHORIZATION_SERVERS" envSeparator:"," yaml:"authorizationServers"`
	// ResourceMetadataURL is the absolute URL of this resource's metadata
	// document; when set, Middleware emits it as the WWW-Authenticate
	// `resource_metadata=` hint on 401 (RFC 9728 §5.1).
	ResourceMetadataURL string `env:"LEARTECH_AUTH_RESOURCE_METADATA_URL" yaml:"resourceMetadataURL"`
}

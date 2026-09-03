package auth

// Config holds authentication configuration for connecting to Hydra.
//
// Auth is MANDATORY. There is no runtime way to disable auth: constructing a
// ServiceAuthClient without ServerURL, ClientID, ClientSecret, or Audience
// returns an error and refuses to start. This is a deliberate hard contract
// so a mis-wired service fails at boot rather than silently accepting
// unvalidated tokens.
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
//	- name: LEARTECH_AUTH_AUDIENCE
//	  value: "<this-service-name>"
type Config struct {
	// ServerURL is the Hydra public URL for the cluster (set via
	// LEARTECH_AUTH_SERVER_URL). Used as the issuer and the JWKS source.
	// REQUIRED — empty at construction returns an error.
	ServerURL string `env:"LEARTECH_AUTH_SERVER_URL" yaml:"serverURL"`
	// ClientID for the OAuth2 client_credentials flow. REQUIRED — empty at
	// construction returns an error.
	ClientID string `env:"LEARTECH_AUTH_CLIENT_ID" yaml:"clientID"`
	// ClientSecret for the OAuth2 client_credentials flow. REQUIRED — empty
	// at construction returns an error.
	ClientSecret string `env:"LEARTECH_AUTH_CLIENT_SECRET" yaml:"clientSecret"`
	// TargetAudience is the OUTBOUND audience requested when minting a
	// client_credentials token (RFC 8707) — the callee this service calls.
	// Hydra binds the minted token's `aud` to it (verified: without it,
	// client_credentials mints aud=[]). Empty = don't request an audience.
	// Distinct from Audience below, which is this service's OWN inbound aud.
	// §A-full will add a per-call audience for addressing multiple callees.
	TargetAudience string `env:"LEARTECH_AUTH_TARGET_AUDIENCE" yaml:"targetAudience"`
	// Audience is this service's own audience identifier (per RFC 8707).
	// Inbound JWT validation requires the token's `aud` claim to contain
	// this value — rejecting tokens issued for other services even when
	// they're signed by the same Hydra. Mirrors rust + dotnet templates'
	// audience-binding behaviour.
	//
	// REQUIRED — empty at construction returns an error. The historical
	// lenient "warn and accept" behaviour was removed as part of the auth
	// hardening (A1): a mis-configured audience must fail-closed at boot,
	// never fail-open at runtime.
	Audience string `env:"LEARTECH_AUTH_AUDIENCE" yaml:"audience"`
	// RequiredScopes is the config-driven required scope(s) for inbound s2s
	// routes, checked via RequireScopes(NewScopes(cfg.RequiredScopes)). Keeps
	// external/partner scopes config, not code (vs the hard-coded HasInternal-
	// Service helper). Comma-separated in env.
	RequiredScopes []string `env:"LEARTECH_AUTH_REQUIRED_SCOPES" envSeparator:"," yaml:"requiredScopes"`

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
	// ScopesSupported are the scopes a client must request to use this
	// resource (RFC 9728 §3.1 `scopes_supported`), published in the metadata
	// document and echoed as `scope=` on the 401 challenge.
	//
	// Set this to the scopes the service ACTUALLY enforces. Hydra's own
	// discovery advertises only openid/offline/offline_access and cannot know
	// about custom scopes, so without this a conforming client has no way to
	// learn what to request — it registers with the OIDC basics and is then
	// refused by every scope-gated route. Derive it from the same constants
	// the route gates use; a hand-kept second list is how the two drift.
	ScopesSupported []string `env:"LEARTECH_AUTH_SCOPES_SUPPORTED" envSeparator:"," yaml:"scopesSupported"`
}

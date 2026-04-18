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
	// ServerURL is the Hydra public URL (e.g. https://hydra-jx-staging.jx.leartech.com)
	ServerURL string `env:"LEARTECH_AUTH_SERVER_URL" yaml:"serverURL"`
	// ClientID for the OAuth2 client_credentials flow
	ClientID string `env:"LEARTECH_AUTH_CLIENT_ID" yaml:"clientID"`
	// ClientSecret for the OAuth2 client_credentials flow
	ClientSecret string `env:"LEARTECH_AUTH_CLIENT_SECRET" yaml:"clientSecret"`
	// DisableMiddleware stops endpoint auth checks (local dev only, never in prod)
	DisableMiddleware bool `yaml:"disableMiddleware"`
}

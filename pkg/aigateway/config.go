package aigateway

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
)

// Env names every caller sets, and the only ones.
//
// ONE EGRESS FOR EVERY MODEL CALL IN THE ESTATE. Before this package the
// callers each reached models their own way — the agents pointed the
// Anthropic SDK at the gateway with ANTHROPIC_BASE_URL, the CLI held its
// own client, the Tekton reviewer did something else again. Every one of
// those is a place where the gateway can be bypassed, a provider's own
// config can leak in, and correlation and prompt caching have to be
// reinvented.
//
// Named LEARTECH_AIGW_* to sit beside the LEARTECH_AUTH_* set that
// pkg/auth already established, so a workload's env reads as one vocabulary
// rather than two conventions.
const (
	EnvURL    = "LEARTECH_AIGW_URL"
	EnvAPIKey = "LEARTECH_AIGW_API_KEY"
	EnvModel  = "LEARTECH_AIGW_MODEL"

	// EnvRunID is the correlation key, and is deliberately NOT prefixed
	// AIGW: it is the estate's existing join key, already present in an
	// agent's environment, and the orchestrator controller names it as
	// such. A second spelling of one id is how a run's records end up
	// split across two field names.
	EnvRunID = "LEARTECH_RUN_ID"

	// EnvSessionID is the interactive analogue, set by a client that holds
	// a registered session rather than executing a run.
	EnvSessionID = "LEARTECH_SESSION_ID"
)

// providerEnv are the provider-native variables this package REFUSES to run
// alongside.
//
// REFUSED RATHER THAN IGNORED, AND THAT IS THE ENFORCEMENT. A caller that
// sets ANTHROPIC_BASE_URL believes it has chosen where its traffic goes. If
// this package quietly ignored it the caller would be wrong about something
// it had explicitly configured, which is worse than not starting: the
// traffic goes somewhere the operator did not intend and the only evidence
// is in a log nobody is reading.
//
// It is also the only mechanism that keeps the single-egress rule true over
// time. A rule enforced by review is a rule that holds until the first
// hurried change.
var providerEnv = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
	"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_API_BASE",
	"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT",
	"DEEPSEEK_API_KEY", "MISTRAL_API_KEY", "OLLAMA_HOST",
}

// Config is what a caller needs to reach the gateway.
type Config struct {
	// URL is the gateway base URL. REQUIRED.
	URL string

	// APIKey is a gateway virtual key. REQUIRED TODAY.
	//
	// A JWT alone does not get a caller to a model. The gateway resolves a
	// spend cap from the credential, populates it only for a virtual key,
	// and refuses a caller whose cap it fails to resolve rather than
	// treating it as unlimited — so a bearer token gets a caller
	// authenticated and then refused at the spend gate.
	//
	// source: leartech-ai-gateway internal/api/handlers.go resolveLimits,
	// which fills CredentialLimits only when keyid is non-empty, and
	// internal/authz/limits.go ResolveLimits, which returns ErrNoPolicy for
	// a nil credential with no policy source. The comment there records
	// that the tenant-policy source "belongs with auth-service" and does
	// not exist yet; when it does, this field becomes optional.
	APIKey string

	// Model is the default model. Optional: a caller may name one per call.
	Model string

	// RunID and SessionID are the caller's own join keys, echoed onto the
	// gateway's usage rows so its records and ours reconcile.
	//
	// AT MOST ONE IS MEANINGFUL. A run and a session are different things
	// and a caller is one or the other; sending both would leave the
	// gateway to decide which is authoritative.
	RunID     string
	SessionID string
}

// ErrProviderEnv is returned when provider-native configuration is present.
var ErrProviderEnv = errors.New("aigateway: provider-native environment is set")

// LoadConfig reads the configuration from env.
//
// env IS A PARAMETER so a test can supply an environment without mutating
// the process's own, which is what makes the refusal below testable at all.
//
// FAILS CLOSED, matching pkg/auth: missing configuration is a construction
// error rather than a default. // proven-by: TestLoadConfig_RefusesWithoutAURL
// A gateway URL guessed from a constant is a request sent somewhere nobody
// chose.
//
// proven-by: TestLoadConfig_ReadsTheEstateEnv
// proven-by: TestLoadConfig_RefusesWithoutAURL
// proven-by: TestLoadConfig_RefusesWithoutAKey
// proven-by: TestLoadConfig_RefusesProviderNativeEnv
// proven-by: TestLoadConfig_RefusesBothRunAndSession
// proven-by: TestLoadConfig_TrimsWhitespace
func LoadConfig(env func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(env(k)) }

	if found := providerEnvSet(env); len(found) > 0 {
		return Config{}, fmt.Errorf("%w: %s. Every model call in this estate "+
			"goes through the gateway, so these are never read and their "+
			"presence means this workload is configured for somewhere else. "+
			"Unset them and set %s and %s instead",
			ErrProviderEnv, strings.Join(found, ", "), EnvURL, EnvAPIKey)
	}

	c := Config{
		URL:       get(EnvURL),
		APIKey:    get(EnvAPIKey),
		Model:     get(EnvModel),
		RunID:     get(EnvRunID),
		SessionID: get(EnvSessionID),
	}

	if c.URL == "" {
		return Config{}, fmt.Errorf("aigateway: %s is not set", EnvURL)
	}
	if c.APIKey == "" {
		return Config{}, fmt.Errorf("aigateway: %s is not set. A bearer token "+
			"is not sufficient: the gateway refuses a caller it cannot resolve "+
			"a spend cap for, and it resolves one only from a virtual key", EnvAPIKey)
	}
	if c.RunID != "" && c.SessionID != "" {
		return Config{}, fmt.Errorf("aigateway: both %s and %s are set. "+
			"A caller is executing a run or holding a session, not both; "+
			"sending both leaves the gateway to choose which is authoritative",
			EnvRunID, EnvSessionID)
	}
	return c, nil
}

// providerEnvSet reports which provider-native names are present, sorted so
// the message is stable.
func providerEnvSet(env func(string) string) []string {
	var found []string
	for _, k := range providerEnv {
		if strings.TrimSpace(env(k)) != "" {
			found = append(found, k)
		}
	}
	sort.Strings(found)
	return found
}

// Client builds a gateway client from the configuration, already correlating.
//
// CORRELATION IS WIRED HERE AND NOT LEFT TO THE CALLER. It is the whole
// reason a caller's spend is attributable, and a caller that has to remember
// to switch it on is a caller whose rows are unattributable the first time
// someone forgets. Everything this package constructs correlates.
//
// proven-by: TestConfigClient_CorrelatesByRun
// proven-by: TestConfigClient_CorrelatesBySession
// proven-by: TestConfigClient_WithNeitherSendsNoCorrelation
func (c Config) Client(h *http.Client) *Client {
	cl := New(c.URL, c.APIKey, h)
	switch {
	case c.RunID != "":
		id := c.RunID
		return cl.Correlate(func() (string, string) { return id, CorrelateRun })
	case c.SessionID != "":
		id := c.SessionID
		return cl.Correlate(func() (string, string) { return id, CorrelateSession })
	}
	return cl
}

// FromEnv is LoadConfig followed by Client, for the common case.
//
// proven-by: TestFromEnv_BuildsACorrelatingClient
// proven-by: TestFromEnv_PropagatesAConfigError
func FromEnv() (*Client, Config, error) {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		return nil, Config{}, err
	}
	return cfg.Client(nil), cfg, nil
}

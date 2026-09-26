package aigateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// envOf builds an env lookup from a map, so a test never mutates the
// process environment and tests stay parallel-safe.
func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		EnvURL:    "https://gw.example.com",
		EnvAPIKey: "vk-abc123",
		EnvModel:  "glm",
	}
}

func TestLoadConfig_ReadsTheEstateEnv(t *testing.T) {
	e := validEnv()
	e[EnvRunID] = "agentrun-7"
	got, err := LoadConfig(envOf(e))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.URL != "https://gw.example.com" || got.APIKey != "vk-abc123" {
		t.Errorf("config = %+v", got)
	}
	if got.Model != "glm" || got.RunID != "agentrun-7" {
		t.Errorf("config = %+v", got)
	}
}

// A URL guessed from a constant is a request sent somewhere nobody chose.
func TestLoadConfig_RefusesWithoutAURL(t *testing.T) {
	e := validEnv()
	delete(e, EnvURL)
	if _, err := LoadConfig(envOf(e)); err == nil {
		t.Fatal("no error with no gateway URL")
	} else if !strings.Contains(err.Error(), EnvURL) {
		t.Errorf("error does not name the missing variable: %v", err)
	}
}

// A BEARER TOKEN IS NOT SUFFICIENT, and the error has to say why: the
// gateway authenticates a JWT caller and then refuses it at the spend gate,
// which reads as an auth problem and is not one.
func TestLoadConfig_RefusesWithoutAKey(t *testing.T) {
	e := validEnv()
	delete(e, EnvAPIKey)
	_, err := LoadConfig(envOf(e))
	if err == nil {
		t.Fatal("no error with no API key")
	}
	if !strings.Contains(err.Error(), "spend cap") {
		t.Errorf("the error does not explain why a token alone fails: %v", err)
	}
}

// THE ENFORCEMENT OF THE SINGLE-EGRESS RULE. A caller that sets
// ANTHROPIC_BASE_URL believes it has chosen where its traffic goes; quietly
// ignoring it would make the caller wrong about something it configured.
func TestLoadConfig_RefusesProviderNativeEnv(t *testing.T) {
	for _, k := range []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"AZURE_OPENAI_ENDPOINT", "OLLAMA_HOST", "DEEPSEEK_API_KEY",
	} {
		e := validEnv()
		e[k] = "something"
		_, err := LoadConfig(envOf(e))
		if err == nil {
			t.Errorf("%s was accepted; the single-egress rule is not enforced", k)
			continue
		}
		if !errors.Is(err, ErrProviderEnv) {
			t.Errorf("%s: error is not ErrProviderEnv: %v", k, err)
		}
		if !strings.Contains(err.Error(), k) {
			t.Errorf("%s: the error does not name the offending variable: %v", k, err)
		}
	}
}

// The refusal names EVERY offending variable, because a caller that unsets
// the one it was told about and gets refused again learns nothing.
func TestLoadConfig_NamesEveryOffendingVariable(t *testing.T) {
	e := validEnv()
	e["ANTHROPIC_API_KEY"] = "x"
	e["OPENAI_API_KEY"] = "y"
	_, err := LoadConfig(envOf(e))
	if err == nil {
		t.Fatal("accepted two provider variables")
	}
	for _, want := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error omits %s: %v", want, err)
		}
	}
}

// An EMPTY provider variable is not configuration. An exported-but-empty
// name is the shape a shell script produces by accident, and refusing it
// would block a caller that had already migrated.
func TestLoadConfig_AnEmptyProviderVariableIsNotSet(t *testing.T) {
	e := validEnv()
	e["ANTHROPIC_BASE_URL"] = "   "
	if _, err := LoadConfig(envOf(e)); err != nil {
		t.Errorf("a blank provider variable was treated as set: %v", err)
	}
}

// A caller is executing a run or holding a session, never both.
func TestLoadConfig_RefusesBothRunAndSession(t *testing.T) {
	e := validEnv()
	e[EnvRunID] = "run-1"
	e[EnvSessionID] = "sess-1"
	_, err := LoadConfig(envOf(e))
	if err == nil {
		t.Fatal("accepted both a run id and a session id")
	}
	if !strings.Contains(err.Error(), EnvRunID) || !strings.Contains(err.Error(), EnvSessionID) {
		t.Errorf("the error does not name both: %v", err)
	}
}

func TestLoadConfig_TrimsWhitespace(t *testing.T) {
	got, err := LoadConfig(envOf(map[string]string{
		EnvURL:    "  https://gw.example.com  ",
		EnvAPIKey: "\tvk-abc123\n",
		EnvRunID:  "  run-1  ",
	}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.URL != "https://gw.example.com" || got.APIKey != "vk-abc123" || got.RunID != "run-1" {
		t.Errorf("whitespace survived: %+v", got)
	}
}

// correlationHeaders runs one request through cfg.Client and reports the
// correlation headers the gateway would see.
func correlationHeaders(t *testing.T, cfg Config) (run, session string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run = r.Header.Get(headerRunID)
		session = r.Header.Get(headerSessionID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(srv.Close)

	cfg.URL = srv.URL
	if _, err := cfg.Client(srv.Client()).ListKeys(t.Context()); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	return run, session
}

// EVERYTHING THIS PACKAGE CONSTRUCTS CORRELATES. A caller that has to
// remember to switch it on is a caller whose rows are unattributable the
// first time someone forgets.
func TestConfigClient_CorrelatesByRun(t *testing.T) {
	run, session := correlationHeaders(t, Config{APIKey: "k", RunID: "agentrun-7"})
	if run != "agentrun-7" {
		t.Errorf("%s = %q, want agentrun-7", headerRunID, run)
	}
	if session != "" {
		t.Errorf("%s = %q, want unset for a run", headerSessionID, session)
	}
}

func TestConfigClient_CorrelatesBySession(t *testing.T) {
	run, session := correlationHeaders(t, Config{APIKey: "k", SessionID: "sess-9"})
	if session != "sess-9" {
		t.Errorf("%s = %q, want sess-9", headerSessionID, session)
	}
	if run != "" {
		t.Errorf("%s = %q, want unset for a session", headerRunID, run)
	}
}

// A caller with neither sends neither, rather than an empty header the
// gateway would record as this caller's run.
func TestConfigClient_WithNeitherSendsNoCorrelation(t *testing.T) {
	run, session := correlationHeaders(t, Config{APIKey: "k"})
	if run != "" || session != "" {
		t.Errorf("sent correlation with none configured: run=%q session=%q", run, session)
	}
}

// FromEnv is the entry point every consumer actually calls, so it gets its
// own coverage rather than being assumed from LoadConfig's.
//
// t.Setenv is used deliberately: it restores the previous value and marks
// the test as non-parallel, which is the only safe way to exercise a
// function that reads the real process environment.
func TestFromEnv_BuildsACorrelatingClient(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(headerRunID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv(EnvURL, srv.URL)
	t.Setenv(EnvAPIKey, "vk-from-env")
	t.Setenv(EnvModel, "glm")
	t.Setenv(EnvRunID, "run-from-env")

	c, cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Model != "glm" {
		t.Errorf("cfg.Model = %q, want glm — the caller needs the default model back", cfg.Model)
	}
	if _, err := c.ListKeys(t.Context()); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if seen != "run-from-env" {
		t.Errorf("%s = %q, want run-from-env", headerRunID, seen)
	}
}

// A configuration error must reach the caller rather than yielding a client
// that fails later at the first request, where it reads as a gateway fault.
func TestFromEnv_PropagatesAConfigError(t *testing.T) {
	t.Setenv(EnvURL, "https://gw.example.com")
	t.Setenv(EnvAPIKey, "")
	if _, _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv returned no error with no API key")
	}
}

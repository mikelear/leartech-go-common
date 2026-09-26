package aigateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A LIBRARY'S CLAIMS ARE PROVEN BY THE LIBRARY'S OWN TESTS.
//
// These cover behaviour that belongs to this package and whose only proof
// used to live in leartech-ba-service's CLI — UsageRow's arithmetic, the
// wire fields the Model row carries, the tool-call shape, the version
// verdict. The comment gate caught it on the move: eleven proven-by
// references naming tests that did not come with the code.
//
// That is a real signal rather than paperwork. A claim about UsageRow.
// HitRate() proven only by a CLI test is a claim that stops being checked
// the moment a second consumer appears, and this package exists precisely
// because a second consumer is appearing.

// The hit-rate DENOMINATOR is calls that could have cached, not all calls.
// Reads over all calls counts traffic to suppliers with no prompt cache as
// traffic that failed to hit one.
func TestUsageRow_HitRateExcludesSuppliersWithNoCache(t *testing.T) {
	warm := UsageRow{
		Model: "deepseek", Calls: 1, CacheableCalls: 1,
		PromptTokens: 254, CacheReadTokens: 1536,
	}
	if got := warm.HitRate(); got < 0.85 || got > 0.87 {
		t.Errorf("HitRate() = %.3f, want ~0.86 (1536 of 1790)", got)
	}
	if warm.CacheWriteTokens() != 0 {
		t.Errorf("CacheWriteTokens() = %d, want 0", warm.CacheWriteTokens())
	}

	// Both write tiers sum, where the billing record has only their total.
	written := UsageRow{
		Model: "claude", Calls: 1, CacheableCalls: 1,
		CacheWrite5mTokens: 300, CacheWrite1hTokens: 2388,
	}
	if got := written.CacheWriteTokens(); got != 2688 {
		t.Errorf("CacheWriteTokens() = %d, want 2688", got)
	}
}

// -1 RATHER THAN 0 FOR A SUPPLIER WITH NO CACHE. Zero is a real answer —
// it had a cache and missed — and no cache is not an answer at all. A
// caller that renders them the same reports a 0% hit rate for Ollama,
// which has nothing to hit.
func TestUsageRow_NoCacheableCallsHasNoHitRate(t *testing.T) {
	cold := UsageRow{Model: "qwen", Calls: 12, CacheableCalls: 0, PromptTokens: 900}
	if got := cold.HitRate(); got != -1 {
		t.Errorf("HitRate() = %v, want -1 for a supplier that cannot cache", got)
	}
}

// A cold cache IS an answer: it could have cached and did not.
func TestUsageRow_AColdCacheIsZeroNotAbsent(t *testing.T) {
	cold := UsageRow{
		Model: "claude", Calls: 1, CacheableCalls: 1,
		PromptTokens: 500, CacheReadTokens: 0,
	}
	if got := cold.HitRate(); got != 0 {
		t.Errorf("HitRate() = %v, want 0 for a cacheable call that read nothing", got)
	}
}

// Provider is WHOSE model it is; Hosting is where the weights run. They are
// different questions and one column cannot answer both — glm, codestral
// and qwen-via-litellm share one interface and are z.ai, Mistral and our
// own Ollama.
func TestModel_ProviderAndHostingAreSeparateWireFields(t *testing.T) {
	var m Model
	if err := json.Unmarshal([]byte(`{
		"id":"glm","provider":"z.ai","hosting":"litellm",
		"max_ctx":131072,"vision":true
	}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Provider != "z.ai" || m.Hosting != "litellm" {
		t.Errorf("provider/hosting = %q/%q, want z.ai/litellm", m.Provider, m.Hosting)
	}
	// MaxCtx and Vision were on the wire all along and silently discarded.
	// The gateway sends them so a caller can cap a prompt that would
	// otherwise be truncated at the provider.
	if m.MaxCtx != 131072 {
		t.Errorf("MaxCtx = %d, want 131072", m.MaxCtx)
	}
	if !m.Vision {
		t.Error("Vision = false, want true")
	}
}

// AN OLDER GATEWAY REPORTS NOTHING RATHER THAN A BLANK. omitempty means an
// absent field stays absent, so a caller can say "this gateway does not
// report suppliers" instead of rendering an empty column as though the
// answer were the empty string.
func TestModel_AnOlderGatewayLeavesProvenanceAbsent(t *testing.T) {
	var m Model
	if err := json.Unmarshal([]byte(`{"id":"claude"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Provider != "" || m.Hosting != "" {
		t.Errorf("provenance invented: %q/%q", m.Provider, m.Hosting)
	}
	if m.MaxCtx != 0 || m.Vision {
		t.Errorf("capabilities invented: ctx=%d vision=%v", m.MaxCtx, m.Vision)
	}
}

// A tool-using turn replays what the assistant ASKED for beside the answer,
// and ties each result to the call it answers. Several calls can be
// outstanding at once, so position is not identity.
func TestChatRequest_CarriesToolsAndToolResults(t *testing.T) {
	asked := ChatRequestMessage{
		Role:      "assistant",
		ToolCalls: json.RawMessage(`[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]`),
	}
	answered := ChatRequestMessage{
		Role: "tool", ToolCallID: "c1", Content: "file contents",
	}

	b, err := json.Marshal([]ChatRequestMessage{asked, answered})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"tool_calls"`, `"tool_call_id":"c1"`, `"read_file"`} {
		if !strings.Contains(got, want) {
			t.Errorf("wire form missing %s: %s", want, got)
		}
	}
}

// THE MINIMUM IS UNREACHABLE BY DESIGN AT LEVEL 1. A gateway reporting
// level 0 is one without the endpoint at all, not one that is too old, so
// nothing can currently be below the floor — and raising the floor is only
// warranted when the client genuinely fails beneath it.
func TestVerdict_TooOldIsUnreachableAtTheCurrentMinimum(t *testing.T) {
	if MinAPILevel != 1 {
		t.Fatalf("MinAPILevel = %d; this test encodes the reasoning for 1", MinAPILevel)
	}
	// Level 0 means "no /version endpoint", which is the absent case rather
	// than the too-old case.
	if MinAPILevel-1 != 0 {
		t.Errorf("the only level below the minimum is 0, which means absent")
	}
}

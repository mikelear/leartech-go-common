package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// These bodies are the gateway's OWN field names, taken from
// internal/api/admin.go (keyView, CreateKeyResponse) and
// internal/store/store.go (UsageRow) at 262398a.
//
// They are fully populated ON PURPOSE. A JSON tag that does not match leaves
// its field at the zero value and decoding still succeeds, so a test that
// checks one or two fields passes with the rest silently empty. The
// assertion below is that NOTHING is left at its zero value — which is what
// makes a wrong tag visible.
//
// This was not hypothetical. UsageRow was first written here as requests /
// input_tokens / output_tokens against a server sending calls /
// prompt_tokens / completion_tokens. Every usage figure would have printed
// as 0, and 0 is a number a reader believes.
const goldenKey = `{
  "keyid": "k-123",
  "name": "ba-cli",
  "scopes": ["leartechapi:gateway:keys_read"],
  "model_allowlist": ["claude-sonnet-4"],
  "rate_limit_rpm": 60,
  "budget_micros": 5000000,
  "expires_at": "2027-01-01T00:00:00Z",
  "revoked_at": "2026-12-01T00:00:00Z",
  "created_by": "mike.lear@leartech.com",
  "created_at": "2026-01-01T00:00:00Z",
  "last_used_at": "2026-06-01T00:00:00Z"
}`

const goldenUsage = `{
  "since": "2026-01-01T00:00:00Z",
  "rows": [{
    "keyid": "k-123",
    "model": "claude-sonnet-4",
    "calls": 42,
    "prompt_tokens": 1000,
    "completion_tokens": 2000,
    "cost_micros": 31415,
    "cache_read_tokens": 1536,
    "cache_write_5m_tokens": 300,
    "cache_write_1h_tokens": 2388,
    "cacheable_calls": 7
  }]
}`

func requireNoZeroFields(t *testing.T, v any, what string) {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		if rv.Field(i).IsZero() {
			t.Errorf("%s.%s is the zero value after decoding a fully populated body — "+
				"the json tag %q does not match what the gateway sends",
				what, rt.Field(i).Name, rt.Field(i).Tag.Get("json"))
		}
	}
}

func TestKey_DecodesEveryFieldTheGatewaySends(t *testing.T) {
	var k Key
	if err := json.Unmarshal([]byte(goldenKey), &k); err != nil {
		t.Fatalf("decode: %v", err)
	}
	requireNoZeroFields(t, k, "Key")
}

func TestUsageRow_DecodesEveryFieldTheGatewaySends(t *testing.T) {
	var u UsageResponse
	if err := json.Unmarshal([]byte(goldenUsage), &u); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if u.Since.IsZero() {
		t.Error("UsageResponse.Since is zero")
	}
	if len(u.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(u.Rows))
	}
	requireNoZeroFields(t, u.Rows[0], "UsageRow")
}

// An empty model_allowlist DENIES every model.
//
// Pinned as a matched pair because the two readings are opposites, and the
// wrong one shipped: this test previously asserted the inverse and passed,
// because it was written from the same misreading as the comment above the
// function it tested. Its stated justification -- "six keys were reported as
// having no model access when they had all of them" -- described an incident
// that did not happen.
//
// So the direction is fixed here against the migration, not against belief:
//
// "An empty model_allowlist used to mean every model in the tenant catalog."
// "It now means DENY, because absence must never be the permissive branch."
//
// source: leartech-ai-gateway migrations/00013_model_allowlist_explicit.sql
func TestKey_AnEmptyAllowlistDeniesEveryModel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantDeny bool
	}{
		{"absent", `{"keyid":"k"}`, true},
		{"empty array", `{"keyid":"k","model_allowlist":[]}`, true},
		{"one model", `{"keyid":"k","model_allowlist":["claude-sonnet-4"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var k Key
			if err := json.Unmarshal([]byte(tc.body), &k); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := k.DeniesEveryModel(); got != tc.wantDeny {
				t.Errorf("DeniesEveryModel() = %v, want %v for %s", got, tc.wantDeny, tc.body)
			}
		})
	}
}

// revoked_at absent is live; present is revoked. A revoked key that reads as
// live is how a key was reported usable when it was not.
func TestKey_RevokedIsDrivenByThePresenceOfRevokedAt(t *testing.T) {
	var live, dead Key
	if err := json.Unmarshal([]byte(`{"keyid":"k"}`), &live); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"keyid":"k","revoked_at":"2026-01-01T00:00:00Z"}`), &dead); err != nil {
		t.Fatal(err)
	}
	if live.Revoked() {
		t.Error("a key with no revoked_at reads as revoked")
	}
	if !dead.Revoked() {
		t.Error("a key with revoked_at reads as live")
	}
}

// An amend sends ONLY the fields the user named. Every field is a pointer
// with omitempty so an unset one is absent from the wire rather than sent as
// null or zero — PATCH with "budget_micros": 0 would zero a live budget.
func TestAmendKeyRequest_OmitsWhatWasNotAskedFor(t *testing.T) {
	name := "after"
	b, err := json.Marshal(AmendKeyRequest{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("an amend naming only the name sent %v — every extra field overwrites a live setting", got)
	}
	if got["name"] != "after" {
		t.Errorf("name = %v", got["name"])
	}

	// The matched pair: a zero that WAS asked for must still be sent.
	var zero int64
	b, _ = json.Marshal(AmendKeyRequest{BudgetMicros: &zero})
	_ = json.Unmarshal(b, &got)
	if _, ok := got["budget_micros"]; !ok {
		t.Error("an explicit budget of 0 was dropped — omitempty is eliding a deliberate value")
	}
}

// The amend response is a keys LIST. Decoding it as a bare key yielded a
// zero-valued Key, and `keys amend` printed that zero value as fact: a key
// just given a $750 ceiling and five models was reported as
// "NONE (empty list denies)" with an "unlimited" budget.
//
// That is the worst direction twice over -- it denies having narrowed the key
// AND claims the ceiling was removed -- on the confirmation of a privileged
// change.
//
// source: leartech-ai-gateway internal/api/admin.go AmendKey
func TestAmendKey_DecodesTheListShapeTheGatewayReturns(t *testing.T) {
	// The gateway's own body, from admin.go's ListKeysResponse{Keys: []keyView{...}}.
	const body = `{"keys":[{
	  "keyid":"702b03ccd4b6","name":"ai-review-worker",
	  "model_allowlist":["claude","deepseek","azure-openai","qwen","codestral"],
	  "budget_micros":750000000,"rate_limit_rpm":60,
	  "scopes":["leartechapi:gateway:chat"],
	  "expires_at":"2027-01-01T00:00:00Z"
	}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "t", srv.Client()).
		AmendKey(context.Background(), "702b03ccd4b6", AmendKeyRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyID != "702b03ccd4b6" || got.Name != "ai-review-worker" {
		t.Fatalf("decoded nothing useful: %+v", got)
	}
	if got.DeniesEveryModel() {
		t.Error("a key with five models reported as denying every model")
	}
	if len(got.ModelAllowlist) != 5 {
		t.Errorf("allowlist = %v, want 5 models", got.ModelAllowlist)
	}
	if got.BudgetMicros == nil || *got.BudgetMicros != 750_000_000 {
		t.Errorf("budget = %v, want 750000000 (not unlimited)", got.BudgetMicros)
	}
}

// No key in the response is an error, not a zero-valued key.
//
// Returning Key{} here is how the previous shape bug rendered: silently, as
// a confident description of a key that denies everything.
func TestAmendKey_AnEmptyKeyListIsAnErrorNotAZeroKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t", srv.Client()).
		AmendKey(context.Background(), "k-1", AmendKeyRequest{})
	if err == nil {
		t.Fatal("an empty list must not decode to a usable key")
	}
	if !strings.Contains(err.Error(), "k-1") {
		t.Errorf("error does not name the key: %v", err)
	}
}

// The inverse wire-shape guard: a field the GATEWAY SENDS that this client
// never declares.
//
// wireshape_test.go supplies a body per endpoint and asserts every field of
// the decoded struct is populated. That catches a struct field the gateway
// does not fill. It cannot catch the opposite and more dangerous case:
// json.Decode drops an undeclared key silently, every declared field still
// populates, and that test passes while the value is lost.
//
// Which is exactly how prompt caching arrived. The gateway added cache
// counters to its usage reporting; this client declared prompt_tokens,
// completion_tokens and cost_micros, so a cached call displayed as costing
// what an uncached one of the same size costs and nothing here failed.
//
// TWO ENDPOINTS, TWO CONVENTIONS, and that is settled rather than an
// oversight:
//
//	/v1/chat/completions  nested — OpenAI's prompt_tokens_details.cached_tokens
//	                      for reads, a leartech_cache namespace for writes, so
//	                      an OpenAI client reads it with no special-casing
//	/admin/v1/usage       flat — cache_read_tokens, cache_write_5m_tokens,
//	                      cache_write_1h_tokens, being our own reporting shape
//
// A fixture written for one against the other fails for a real-looking wrong
// reason, so both below are copied from the captures in wireshape_test.go.
package aigateway

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// consumesEveryField reports each key in a body that the target declares no
// field for.
//
// A helper rather than one test, because the same check has to run for every
// endpoint whose shape the gateway can extend, and a bespoke test per endpoint
// is how one gets forgotten.
//
// proven-by: TestConsumesEveryField_CatchesADroppedField
// proven-by: TestConsumesEveryField_RefusesAnEmptyFixture
func consumesEveryField(t *testing.T, what, body string, into any, ignored map[string]string) {
	t.Helper()

	var whole map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &whole); err != nil {
		t.Fatalf("%s: fixture is not an object: %v", what, err)
	}
	if len(whole) == 0 {
		// An empty fixture would examine nothing and pass. // proven-by: TestConsumesEveryField_RefusesAnEmptyFixture
		t.Fatalf("%s: fixture is empty", what)
	}

	// One key at a time, so the report names EVERY unconsumed field rather
	// than whichever the decoder reached first. Fixing them one per round
	// trip is how three fields take three pull requests.
	var unconsumed []string
	for k, v := range whole {
		if _, ok := ignored[k]; ok {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(`{"` + k + `":` + string(v) + `}`))
		dec.DisallowUnknownFields()
		if err := dec.Decode(into); err != nil {
			unconsumed = append(unconsumed, k)
		}
	}
	sort.Strings(unconsumed)
	if len(unconsumed) > 0 {
		t.Errorf("%s: the gateway sends %v and this client declares no field "+
			"for them.\njson.Decode drops an undeclared key silently, so every "+
			"declared field still populates and the fixture-driven wire-shape "+
			"test passes while the value is lost.", what, unconsumed)
	}
}

// A usage row consumes every field the reporting endpoint sends.
//
// Copied from the GET /admin/v1/usage capture in wireshape_test.go, which is
// the flat convention.
func TestUsageRow_ConsumesEveryFieldTheGatewaySends(t *testing.T) {
	const body = `{"keyid":"k-1","model":"claude","calls":42,
		"prompt_tokens":1000,"completion_tokens":2000,"cost_micros":31415,
		"cache_read_tokens":1536,"cache_write_5m_tokens":300,
		"cache_write_1h_tokens":2388,"cacheable_calls":7}`

	var row UsageRow
	consumesEveryField(t, "usage row", body, &row, nil)
}

// A chat reply's usage consumes them too, and this is the one a human sees.
//
// `ship-proven chat` prints these counts per call, so a field lost here is a
// number a user reads as complete when it is not. Copied from the POST
// /v1/chat/completions capture, which is the nested convention.
func TestChatUsage_ConsumesEveryFieldTheGatewaySends(t *testing.T) {
	const body = `{"prompt_tokens":2815,"completion_tokens":8,"total_tokens":2823,
		"prompt_tokens_details":{"cached_tokens":2800},
		"leartech_cache":{"write_5m_tokens":4,"write_1h_tokens":11}}`

	var u ChatUsage
	consumesEveryField(t, "chat usage", body, &u, nil)
}

// The guard has to fail when a field is dropped, or it is decoration.
func TestConsumesEveryField_CatchesADroppedField(t *testing.T) {
	// Declares prompt_tokens and nothing else, standing in for a struct that
	// has not kept up with the gateway.
	var behind struct {
		PromptTokens int `json:"prompt_tokens"`
	}
	fake := &testing.T{}
	consumesEveryField(fake, "behind", `{"prompt_tokens":1,"cache_read_tokens":2}`, &behind, nil)
	if !fake.Failed() {
		t.Error("a dropped field passed; this guard would never catch the " +
			"thing it exists for")
	}
}

// An empty fixture examines nothing, so it must not pass.
func TestConsumesEveryField_RefusesAnEmptyFixture(t *testing.T) {
	var row UsageRow
	fake := &testing.T{}

	// In its own goroutine: the helper reports this with Fatalf, which is a
	// runtime.Goexit and would end this test rather than be observed.
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumesEveryField(fake, "empty", `{}`, &row, nil)
	}()
	<-done

	if !fake.Failed() {
		t.Error("an empty fixture passed; a check that examines nothing " +
			"reports success forever")
	}
}

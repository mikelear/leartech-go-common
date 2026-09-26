package aigateway

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// knowinglyIgnored is what the gateway sends and this client deliberately
// does not decode.
//
// A NAMED LIST, not a category, on the precedent of conformance's
// ownedPackages: an exemption that is a category rots, a list of names has to
// be argued with. Adding a line here is a decision someone makes in review;
// silently dropping a field is not a decision at all.
var knowinglyIgnored = map[string]string{
	"choices[].message.tool_calls": "the CLI is not an agent loop and does not " +
		"execute tools; ai-gateway#81/#83 made these structured, and if the CLI " +
		"ever grows tool use this line is the thing to delete first",
	"object":  "an OpenAI compatibility constant, always \"list\" or \"chat.completion\"",
	"created": "a unix timestamp the CLI has no use for; the gateway logs its own",
	"id":      "the completion id; request_id on the gateway's usage line is the join key we use",
}

// Nothing the gateway sends may be dropped without someone having decided to.
//
// THE INVERSE OF requireNoZeroFields, and the gap it leaves. That helper
// asserts every field THIS CLIENT DECLARES is populated by the fixture — it
// caught the provider→interface rename. It cannot see the other direction: a
// field the gateway sends that this client never declared decodes into
// nothing, silently, with no error and no test.
//
// That is the defect shape that has now appeared three times in the gateway
// in two days — #73, #75, #83 — each one "the counters existed and a call
// site did not read them". This is the client-side mirror of it. #74's
// cacheable_calls would have been invisible here had I not been the one
// adding the field.
//
// proven-by is not available to a test, so the guard is the list above: an
// unlisted field fails, which forces the drop to be argued rather than
// discovered later.
func TestWireShape_NothingTheGatewaySendsIsSilentlyDropped(t *testing.T) {
	for _, tc := range droppedFieldCases() {
		t.Run(tc.endpoint, func(t *testing.T) {
			var generic any
			if err := json.Unmarshal([]byte(tc.body), &generic); err != nil {
				t.Fatalf("fixture is not valid JSON: %v", err)
			}
			sent := map[string]bool{}
			collectKeys("", generic, sent)
			if len(sent) == 0 {
				t.Fatal("no keys found in the fixture; this test is reading nothing")
			}

			consumed := map[string]bool{}
			collectTags("", reflect.TypeOf(tc.target), consumed)

			var dropped []string
			for k := range sent {
				if consumed[k] {
					continue
				}
				if _, ok := knowinglyIgnored[k]; ok {
					continue
				}
				// A leaf under an ignored parent is ignored with it.
				var covered bool
				for ign := range knowinglyIgnored {
					if strings.HasPrefix(k, ign+".") {
						covered = true
						break
					}
				}
				if !covered {
					dropped = append(dropped, k)
				}
			}
			sort.Strings(dropped)
			for _, d := range dropped {
				t.Errorf("the gateway sends %q and this client decodes it nowhere.\n"+
					"      Either add the field, or add it to knowinglyIgnored with the "+
					"reason. Dropping it silently is how a counter comes to exist and "+
					"go unread.", d)
			}
		})
	}
}

// collectKeys walks a decoded body and records every leaf path.
func collectKeys(prefix string, v any, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			collectKeys(p, sub, out)
		}
	case []any:
		for _, sub := range t {
			collectKeys(prefix+"[]", sub, out)
		}
	}
}

// collectTags walks a struct and records every json path it can consume.
func collectTags(prefix string, rt reflect.Type, out map[string]bool) {
	for rt != nil && rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt == nil || rt.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		p := tag
		if prefix != "" {
			p = prefix + "." + tag
		}
		out[p] = true

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice {
			collectTags(p+"[]", ft.Elem(), out)
			continue
		}
		collectTags(p, ft, out)
	}
}

var _ = fmt.Sprintf

// droppedFieldCases pairs each endpoint's golden body with the type this
// client decodes it into.
//
// Separate from the table in wireshape_test.go deliberately: that one asserts
// the client's fields are populated, this one asserts the gateway's fields are
// consumed. Sharing a table would tempt someone to satisfy both by editing
// the fixture, which is the move that hides the defect.
func droppedFieldCases() []struct {
	endpoint string
	body     string
	target   any
} {
	return []struct {
		endpoint string
		body     string
		target   any
	}{
		{"POST /v1/chat/completions", goldenChat, ChatResponse{}},
		{"GET /admin/v1/usage", goldenUsage, UsageResponse{}},
		{"GET /v1/models", goldenModels, modelsResponse{}},
	}
}

// The golden bodies below are built from the GATEWAY's types, not from this
// client's. That direction matters: a fixture derived from the local structs
// can only contain fields the local structs already have, so the test would
// be asking whether this client decodes what this client declares — which is
// always yes, and is exactly the self-confirming fixture that made three
// gateway defects survive their own tests.
//
// source: leartech-ai-gateway @ 6509a51
//
//	internal/api/types.go        Model, ChatCompletionResponse, Choice, ChatMessage
//	internal/store/store.go      UsageRow, UsageResponse
const (
	goldenModels = `{"object":"list","data":[
	  {"id":"glm","object":"model","owned_by":"leartech","interface":"litellm",
	   "provider":"z.ai","hosting":"vendor-api","provider_model":"glm-5.3",
	   "max_ctx":1000000,"vision":false}]}`
)

package aigateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Every endpoint, decoded through its real Client method, asserting that
// nothing the gateway sent is left at its zero value.
//
// This exists because the same defect shipped THREE times, and each time it
// was fixed as a one-off:
//
//   - scp typed as string against an array, so the whole claims parse failed
//     and `whoami` showed subject/client/audience/expires as "(unknown)"
//   - apiError.error typed as string against an object, so the gateway's real
//     403 reason was discarded and the client printed its own guess over it
//   - AmendKey decoding a bare key against {"keys":[...]}, so a successful
//     amend reported the key as having no allowlist and no ceiling
//
// One shape, three symptoms: a field or envelope nobody decodes is
// indistinguishable from one nobody sends, and the zero value that results
// reads as a real answer -- "(unknown)", "lacks the scope", "unlimited".
// Silence rendering as fact is the whole class.
//
// A per-field test cannot catch the next one, because the next one is in
// whichever type is added next. This is per-ENDPOINT and iterates the struct,
// so a new field with a wrong tag fails without anyone writing a new test.
//
// The bodies are the gateway's own, from internal/api/admin.go and
// handlers.go, and are fully populated ON PURPOSE: a wrong tag leaves its
// field at zero and decoding still succeeds, so a test that checks one or two
// fields passes with the rest silently empty.
//
// source: leartech-ai-gateway internal/api/admin.go, internal/api/handlers.go
func TestWireShape_EveryEndpointDecodesWhatTheGatewaySends(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		body     string
		// call returns the decoded value, and the sub-values that also have
		// to be fully populated (elements behind slices).
		call func(*Client) (any, []any, error)
	}{
		{
			endpoint: "GET /admin/v1/keys",
			body: `{"keys":[{"keyid":"k-1","name":"n","scopes":["s"],
			  "model_allowlist":["claude"],"rate_limit_rpm":60,"budget_micros":1,
			  "expires_at":"2027-01-01T00:00:00Z","revoked_at":"2026-12-01T00:00:00Z",
			  "created_by":"m","created_at":"2026-01-01T00:00:00Z",
			  "last_used_at":"2026-06-01T00:00:00Z"}]}`,
			call: func(c *Client) (any, []any, error) {
				ks, err := c.ListKeys(context.Background())
				if err != nil || len(ks) == 0 {
					return nil, nil, err
				}
				return ks[0], nil, nil
			},
		},
		{
			endpoint: "POST /admin/v1/keys",
			body: `{"keyid":"k-1","secret":"sk-lt-x","key":{"keyid":"k-1","name":"n",
			  "scopes":["s"],"model_allowlist":["claude"],"rate_limit_rpm":60,
			  "budget_micros":1,"expires_at":"2027-01-01T00:00:00Z",
			  "revoked_at":"2026-12-01T00:00:00Z","created_by":"m",
			  "created_at":"2026-01-01T00:00:00Z","last_used_at":"2026-06-01T00:00:00Z"}}`,
			call: func(c *Client) (any, []any, error) {
				got, err := c.CreateKey(context.Background(), CreateKeyRequest{})
				return got, []any{got.Key}, err
			},
		},
		{
			// The amend envelope. This is the one that shipped wrong.
			endpoint: "PATCH /admin/v1/keys/{id}",
			body: `{"keys":[{"keyid":"k-1","name":"n","scopes":["s"],
			  "model_allowlist":["claude"],"rate_limit_rpm":60,"budget_micros":1,
			  "expires_at":"2027-01-01T00:00:00Z","revoked_at":"2026-12-01T00:00:00Z",
			  "created_by":"m","created_at":"2026-01-01T00:00:00Z",
			  "last_used_at":"2026-06-01T00:00:00Z"}]}`,
			call: mustAmend,
		},
		{
			endpoint: "GET /admin/v1/usage",
			body: `{"since":"2026-01-01T00:00:00Z","rows":[{"keyid":"k-1",
			  "model":"claude","calls":42,"prompt_tokens":1000,
			  "completion_tokens":2000,"cost_micros":31415,
			  "cache_read_tokens":1536,"cache_write_5m_tokens":300,
			  "cache_write_1h_tokens":2388,"cacheable_calls":7}]}`,
			call: func(c *Client) (any, []any, error) {
				got, err := c.Usage(context.Background())
				if err != nil || len(got.Rows) == 0 {
					return got, nil, err
				}
				return got, []any{got.Rows[0]}, nil
			},
		},
		{
			endpoint: "GET /v1/models",
			body: `{"object":"list","data":[{"id":"claude","object":"model",
			  "owned_by":"leartech","interface":"anthropic",
			  "provider":"anthropic","hosting":"vendor-api",
			  "provider_model":"claude-opus-4-8","max_ctx":200000,"vision":true}]}`,
			call: func(c *Client) (any, []any, error) {
				ms, err := c.Models(context.Background())
				if err != nil || len(ms) == 0 {
					return nil, nil, err
				}
				return ms[0], nil, nil
			},
		},
		{
			endpoint: "POST /v1/chat/completions",
			body: `{"id":"c-1","model":"claude-opus-4-8","choices":[{"index":0,
			  "finish_reason":"stop","message":{"role":"assistant","content":"hi"}}],
			  "usage":{"prompt_tokens":2815,"completion_tokens":8,"total_tokens":2823,
			  "prompt_tokens_details":{"cached_tokens":2800},
			  "leartech_cache":{"write_5m_tokens":4,"write_1h_tokens":11}}}`,
			call: func(c *Client) (any, []any, error) {
				got, err := c.Chat(context.Background(), ChatRequest{})
				if err != nil || len(got.Choices) == 0 {
					return got, nil, err
				}
				return got, []any{got.Usage, got.Choices[0].Message}, nil
			},
		},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			top, nested, err := tc.call(New(srv.URL, "t", srv.Client()))
			if err != nil {
				t.Fatalf("%s: %v", tc.endpoint, err)
			}
			if top == nil {
				t.Fatalf("%s decoded nothing at all", tc.endpoint)
			}
			assertFullyDecoded(t, top, tc.endpoint)
			for _, n := range nested {
				assertFullyDecoded(t, n, tc.endpoint)
			}
		})
	}
}

func mustAmend(c *Client) (any, []any, error) {
	got, err := c.AmendKey(context.Background(), "k-1", AmendKeyRequest{})
	return got, nil, err
}

// assertFullyDecoded fails for any exported field left at its zero value.
//
// Index 0 of a slice is NOT descended into automatically: the caller passes
// nested values explicitly, because a generic walker would recurse into
// time.Time and pointer graphs and report noise. A check with false positives
// gets muted.
func assertFullyDecoded(t *testing.T, v any, endpoint string) {
	t.Helper()
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			t.Fatalf("%s: %T is nil", endpoint, v)
		}
		rv = rv.Elem()
	}
	rt := rv.Type()
	if rt.Kind() != reflect.Struct {
		t.Fatalf("%s: %s is not a struct", endpoint, rt)
	}
	zero := 0
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		if rv.Field(i).IsZero() {
			zero++
			t.Errorf("%s: %s.%s is the zero value after decoding a fully "+
				"populated body — json tag %q does not match the wire",
				endpoint, rt.Name(), f.Name, f.Tag.Get("json"))
		}
	}
	// Every field zero is the ENVELOPE being wrong, not one tag. Said
	// separately because the per-field errors above read as a dozen
	// unrelated typos when the actual cause is one wrong wrapper -- which
	// is exactly how the AmendKey bug looked.
	if zero > 0 && zero == exportedCount(rt) {
		t.Errorf("%s: EVERY field of %s is zero. That is the response "+
			"envelope being wrong, not the field tags.", endpoint, rt.Name())
	}
}

func exportedCount(rt reflect.Type) int {
	n := 0
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).PkgPath == "" {
			n++
		}
	}
	return n
}

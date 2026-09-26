package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The gateway's own chat wire shape, from internal/api/chat.go. Fully
// populated for the reason goldenKey is: a wrong tag decodes to a zero
// value and still succeeds.
const goldenChat = `{
  "id": "chatcmpl-1",
  "model": "claude-opus-4-8",
  "choices": [{
    "index": 0,
    "finish_reason": "stop",
    "message": {"role": "assistant", "content": "the terminal client works"}
  }],
  "usage": {"prompt_tokens": 2815, "completion_tokens": 8, "total_tokens": 2823,
      "prompt_tokens_details": {"cached_tokens": 2800},
      "leartech_cache": {"write_5m_tokens": 4, "write_1h_tokens": 11}}
}`

func TestChatResponse_DecodesEveryFieldTheGatewaySends(t *testing.T) {
	var got ChatResponse
	if err := json.Unmarshal([]byte(goldenChat), &got); err != nil {
		t.Fatal(err)
	}
	requireNoZeroFields(t, got, "ChatResponse")
	requireNoZeroFields(t, got.Usage, "ChatUsage")
	if len(got.Choices) != 1 {
		t.Fatalf("choices: %v", got.Choices)
	}
	requireNoZeroFields(t, got.Choices[0].Message, "ChatMessage")
}

func TestChatResponse_ReplyIsTheAssistantText(t *testing.T) {
	var r ChatResponse
	if err := json.Unmarshal([]byte(goldenChat), &r); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Reply()
	if !ok || got != "the terminal client works" {
		t.Fatalf("Reply() = %q, %v", got, ok)
	}
}

func TestChatResponse_ReplyOnNoChoicesDoesNotPanic(t *testing.T) {
	// A refusal or a filtered completion returns zero choices. Indexing
	// [0] unconditionally would crash the CLI on a legitimate response.
	got, ok := (ChatResponse{}).Reply()
	if ok {
		t.Fatalf("no choices must not report a reply, got %q", got)
	}
}

func TestChat_SendsTheModelAndMessagesTheCallerAsked(t *testing.T) {
	var body ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vk-session" {
			t.Errorf("chat must go as the session key, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(goldenChat))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "vk-session", srv.Client()).Chat(context.Background(), ChatRequest{
		Model:    "claude",
		Messages: []ChatRequestMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.Model != "claude" || len(body.Messages) != 1 || body.Messages[0].Content != "hello" {
		t.Fatalf("request reached the gateway wrong: %+v", body)
	}
}

func TestChat_ModelNotAllowedSurfacesTheGatewaysOwnReason(t *testing.T) {
	// The 403 a wrongly-allowlisted key earns. The CLI printed its own
	// guess ("lacks the scope") over this for a whole session because
	// apiError.Error was typed string against a nested object.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"model \"echo\" is not allowed for this caller","type":"model_not_allowed","code":"403"}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "vk", srv.Client()).Chat(context.Background(), ChatRequest{Model: "echo"})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"not allowed", "echo"} {
		if !contains(err.Error(), want) {
			t.Fatalf("error must carry the gateway's reason, missing %q: %v", want, err)
		}
	}
	if contains(err.Error(), "scope") {
		t.Fatalf("a model refusal is not a scope refusal: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// Serves has three states, because silence is not the same as directness.
//
// A two-state version returned (.., false) for both an absent provider_model
// and an alias equal to its own name, so `ship-proven models` printed "(itself)"
// against a gateway that had reported nothing -- asserting the alias was the
// concrete model on no evidence.
func TestModel_ServesDistinguishesUnreportedFromDirect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model Model
		want  Served
		serve string
	}{
		{"older gateway says nothing", Model{ID: "claude"}, ServedUnreported, ""},
		{"alias is the concrete name", Model{ID: "echo", ProviderModel: "echo"}, ServedDirectly, "echo"},
		{"alias resolves elsewhere",
			Model{ID: "claude", ProviderModel: "claude-opus-4-8"}, ServedIndirectly, "claude-opus-4-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, known := tc.model.Serves()
			if known != tc.want {
				t.Errorf("state = %v, want %v", known, tc.want)
			}
			if got != tc.serve {
				t.Errorf("served = %q, want %q", got, tc.serve)
			}
		})
	}
}

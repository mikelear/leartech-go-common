package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TransformSession POSTs the right path with the request body, and decodes the
// gateway's response.
func TestTransformSession_PostsToTheEndpoint(t *testing.T) {
	var got TransformSessionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/transform" {
			t.Errorf("path = %q, want /v1/sessions/transform", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer vk-session" {
			t.Errorf("transform must go as the session key, got %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"object":"session.transform","target_model":"claude",` +
			`"checkpoint":{"version":1,"turn":1,"messages":[{"Role":"system","Content":"x"}]},` +
			`"report":{"before_tokens":100,"after_tokens":80,"target_max_ctx":200000,"compacted":2,"fit":true,"bridge_added":true}}`))
	}))
	defer srv.Close()

	resp, err := New(srv.URL, "vk-session", srv.Client()).TransformSession(context.Background(),
		TransformSessionRequest{
			TargetModel: "claude",
			SourceModel: "glm",
			Checkpoint:  json.RawMessage(`{"version":1,"turn":1,"messages":[{"Role":"user","Content":"hi"}]}`),
		})
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetModel != "claude" || got.SourceModel != "glm" {
		t.Fatalf("request reached the gateway wrong: %+v", got)
	}
	if resp.Object != "session.transform" || !resp.Report.Fit || !resp.Report.BridgeAdded {
		t.Fatalf("response decoded wrong: %+v", resp)
	}
	if len(resp.Checkpoint) == 0 {
		t.Error("the transformed checkpoint was not carried back")
	}
}

package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SessionHandover POSTs the right path as the session key and decodes the
// gateway's agreed response.
func TestSessionHandover_PostsToTheEndpoint(t *testing.T) {
	var got SessionHandoverRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/handover" {
			t.Errorf("path = %q, want /v1/sessions/handover", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer vk-session" {
			t.Errorf("handover must go as the session key, got %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"object":"session.handover","phase":"agreed","target_model":"claude",` +
			`"checkpoint":{"version":1,"turn":1,"messages":[{"Role":"system","Content":"x"}]},` +
			`"handover":{"phase":"agreed","rounds_used":1,"cap_hit":false,` +
			`"rounds":[{"speaker":"claude","role":"target","content":"summary"},{"speaker":"glm","role":"source","content":"AGREED"}]},` +
			`"transform":{"fit":true,"bridge_added":true}}`))
	}))
	defer srv.Close()

	resp, err := New(srv.URL, "vk-session", srv.Client()).SessionHandover(context.Background(),
		SessionHandoverRequest{
			TargetModel: "claude",
			SourceModel: "glm",
			Checkpoint:  json.RawMessage(`{"version":1,"turn":2,"messages":[{"Role":"user","Content":"hi"}]}`),
		})
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetModel != "claude" || got.SourceModel != "glm" {
		t.Fatalf("request reached the gateway wrong: %+v", got)
	}
	if resp.Phase != HandoverPhaseAgreed {
		t.Fatalf("phase = %q, want agreed", resp.Phase)
	}
	if len(resp.Checkpoint) == 0 {
		t.Error("an agreed handover carried no checkpoint back")
	}
	if resp.Transform == nil || !resp.Transform.BridgeAdded {
		t.Error("the returned checkpoint was not reported as bridged")
	}
	if len(resp.Handover.Rounds) != 2 {
		t.Errorf("audit trail has %d rounds, want 2", len(resp.Handover.Rounds))
	}
}

// Files the caller uploaded are carried in the request body, so the loop's
// second call reaches the gateway with the bytes the target asked for.
func TestSessionHandover_CarriesUploadedFiles(t *testing.T) {
	var got SessionHandoverRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"object":"session.handover","phase":"agreed","target_model":"claude",` +
			`"checkpoint":{"version":1,"turn":1,"messages":[{"Role":"system","Content":"x"}]},` +
			`"handover":{"phase":"agreed"},"transform":{"fit":true,"bridge_added":true}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "vk-session", srv.Client()).SessionHandover(context.Background(),
		SessionHandoverRequest{
			TargetModel: "claude", SourceModel: "glm",
			Checkpoint: json.RawMessage(`{"version":1,"turn":2,"messages":[{"Role":"user","Content":"hi"}]}`),
			Files: []HandoverFile{
				{Path: "NOTES.md", Content: "the refactor split auth into tokens + sessions"},
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "NOTES.md" {
		t.Fatalf("uploaded files did not reach the gateway: %+v", got.Files)
	}
	if !strings.Contains(got.Files[0].Content, "tokens + sessions") {
		t.Error("the file body did not reach the gateway")
	}
}

// A need_files step decodes with its requested paths and no checkpoint — the
// caller's cue to upload and call again.
func TestSessionHandover_DecodesANeedFilesStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"session.handover","phase":"need_files","target_model":"claude",` +
			`"requested_files":["NOTES.md","TODO.md"],` +
			`"handover":{"phase":"need_files","requested_files":["NOTES.md","TODO.md"]}}`))
	}))
	defer srv.Close()

	resp, err := New(srv.URL, "vk-session", srv.Client()).SessionHandover(context.Background(),
		SessionHandoverRequest{TargetModel: "claude", SourceModel: "glm",
			Checkpoint: json.RawMessage(`{"version":1,"turn":2,"messages":[{"Role":"user","Content":"hi"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Phase != HandoverPhaseNeedFiles {
		t.Fatalf("phase = %q, want need_files", resp.Phase)
	}
	if len(resp.RequestedFiles) != 2 || resp.RequestedFiles[0] != "NOTES.md" {
		t.Fatalf("requested files = %v", resp.RequestedFiles)
	}
	if len(resp.Checkpoint) != 0 {
		t.Error("need_files must carry no checkpoint")
	}
}

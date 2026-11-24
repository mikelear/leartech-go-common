package github_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/github"
)

var repo = forge.Repo{Owner: "mikelear", Name: "demo"}

func TestCreateWebhook_SendsGitHubsHookName(t *testing.T) {
	var got map[string]any
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":9,"name":"web","active":true,"events":["push"],"config":{"url":"https://l.example/h","content_type":"json"}}`))
	}))
	defer srv.Close()

	h, err := github.New(srv.URL, "tok", srv.Client()).CreateWebhook(context.Background(), repo,
		forge.WebhookSpec{URL: "https://l.example/h", Secret: "s3cret", Events: []string{"push"}, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/repos/mikelear/demo/hooks" {
		t.Fatalf("%s %s, want POST /repos/mikelear/demo/hooks", gotMethod, gotPath)
	}
	if got["name"] != "web" {
		t.Errorf("name = %v, want the literal \"web\" — GitHub rejects anything else", got["name"])
	}
	cfg, _ := got["config"].(map[string]any)
	if cfg["secret"] != "s3cret" {
		t.Errorf("config.secret = %v, want the HMAC secret", cfg["secret"])
	}
	if cfg["content_type"] != "json" {
		t.Errorf("config.content_type = %v, want json", cfg["content_type"])
	}
	if h.ID != 9 || h.URL != "https://l.example/h" {
		t.Errorf("returned hook = %+v", h)
	}
}

// An unsigned hook omits the secret rather than sending an empty one, which GitHub stores as
// a real (empty) secret and then signs with.
func TestCreateWebhook_OmitsAnEmptySecret(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	if _, err := github.New(srv.URL, "tok", srv.Client()).CreateWebhook(context.Background(), repo,
		forge.WebhookSpec{URL: "https://l.example/h", Active: true}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := got["config"].(map[string]any)
	if _, present := cfg["secret"]; present {
		t.Error("config.secret was sent although the spec has none")
	}
}

func TestListWebhooks_ReadsConfigURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/mikelear/demo/hooks" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":3,"name":"web","active":true,"events":["push","pull_request"],
			"config":{"url":"https://l.example/h","content_type":"json"}}]`))
	}))
	defer srv.Close()

	hooks, err := github.New(srv.URL, "tok", srv.Client()).ListWebhooks(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].URL != "https://l.example/h" || hooks[0].ID != 3 {
		t.Fatalf("hooks = %+v", hooks)
	}
}

func TestDeleteWebhook_UsesTheHookID(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := github.New(srv.URL, "tok", srv.Client()).DeleteWebhook(context.Background(), repo, 42); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || !strings.HasSuffix(gotPath, "/hooks/42") {
		t.Fatalf("%s %s, want DELETE …/hooks/42", gotMethod, gotPath)
	}
}

// A 422 is the forge saying the hook is already there. The shared mapping reads it as a
// conflict so a caller can treat that as "already registered" rather than a failure.
func TestCreateWebhook_DuplicateIsAConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Hook already exists"}`))
	}))
	defer srv.Close()

	_, err := github.New(srv.URL, "tok", srv.Client()).CreateWebhook(context.Background(), repo,
		forge.WebhookSpec{URL: "https://l.example/h", Active: true})
	if err == nil {
		t.Fatal("want an error")
	}
}

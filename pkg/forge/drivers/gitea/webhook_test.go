package gitea_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/gitea"
)

var repo = forge.Repo{Owner: "it-test", Name: "lh-test"}

// Forgejo rejects GitHub's hook name and wants a Gitea hook type. Sending the wrong one is
// the single defect that would make this driver look like it worked — the request succeeds
// elsewhere in the body and the hook is simply not created.
//
// proven-by: TestCreateWebhook_SendsGiteasHookType
func TestCreateWebhook_SendsGiteasHookType(t *testing.T) {
	var got map[string]any
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":11,"type":"gitea","active":true,"events":["push"],
			"config":{"url":"https://l.example/h","content_type":"json"}}`))
	}))
	defer srv.Close()

	h, err := gitea.New(srv.URL, "tok", srv.Client()).CreateWebhook(context.Background(), repo,
		forge.WebhookSpec{URL: "https://l.example/h", Secret: "s3cret", Events: []string{"push"}, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/repos/it-test/lh-test/hooks" {
		t.Fatalf("%s %s, want POST /api/v1/repos/it-test/lh-test/hooks", gotMethod, gotPath)
	}
	if got["type"] != "gitea" {
		t.Errorf("type = %v, want \"gitea\" — Forgejo keeps the Gitea hook-type names", got["type"])
	}
	if _, wrong := got["name"]; wrong {
		t.Error("a \"name\" field was sent; that is GitHub's shape and Gitea ignores it")
	}
	cfg, _ := got["config"].(map[string]any)
	if cfg["secret"] != "s3cret" {
		t.Errorf("config.secret = %v, want the HMAC secret", cfg["secret"])
	}
	if h.ID != 11 {
		t.Errorf("returned hook = %+v", h)
	}
}

func TestCreateWebhook_OmitsAnEmptySecret(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	if _, err := gitea.New(srv.URL, "tok", srv.Client()).CreateWebhook(context.Background(), repo,
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
		if r.URL.Path != "/api/v1/repos/it-test/lh-test/hooks" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":5,"type":"gitea","active":true,"events":["push"],
			"config":{"url":"https://l.example/h","content_type":"json"}}]`))
	}))
	defer srv.Close()

	hooks, err := gitea.New(srv.URL, "tok", srv.Client()).ListWebhooks(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].URL != "https://l.example/h" || hooks[0].ID != 5 {
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

	if err := gitea.New(srv.URL, "tok", srv.Client()).DeleteWebhook(context.Background(), repo, 42); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || !strings.HasSuffix(gotPath, "/hooks/42") {
		t.Fatalf("%s %s, want DELETE …/hooks/42", gotMethod, gotPath)
	}
}

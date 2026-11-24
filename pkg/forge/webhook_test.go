package forge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
)

// The fake records what the shared ensure logic asked a driver to do, so the logic can be
// tested once rather than twice (once per driver).
type fakeWebhooks struct {
	hooks   []forge.Webhook
	nextID  int64
	created []forge.WebhookSpec
	deleted []int64
	failOn  string
}

func (f *fakeWebhooks) ListWebhooks(_ context.Context, _ forge.Repo) ([]forge.Webhook, error) {
	if f.failOn == "list" {
		return nil, errors.New("list failed")
	}
	return f.hooks, nil
}

func (f *fakeWebhooks) CreateWebhook(_ context.Context, _ forge.Repo, s forge.WebhookSpec) (forge.Webhook, error) {
	f.nextID++
	f.created = append(f.created, s)
	h := forge.Webhook{ID: f.nextID, URL: s.URL, Active: s.Active, Events: s.Events}
	f.hooks = append(f.hooks, h)
	return h, nil
}

func (f *fakeWebhooks) DeleteWebhook(_ context.Context, _ forge.Repo, id int64) error {
	f.deleted = append(f.deleted, id)
	for i, h := range f.hooks {
		if h.ID == id {
			f.hooks = append(f.hooks[:i], f.hooks[i+1:]...)
			break
		}
	}
	return nil
}

var spec = forge.WebhookSpec{
	URL:    "https://lighthouse.example/hook",
	Secret: "s3cret",
	Events: []string{"push", "pull_request"},
	Active: true,
}

var repo = forge.Repo{Owner: "mikelear", Name: "demo"}

// The whole reason the ensure is idempotent by URL: a forge answers 422 for a duplicate,
// which cannot be told apart from a rejected payload without listing first. A second
// identical call must not create a second hook.
//
// proven-by: TestEnsureWebhook_IsIdempotentByURL
func TestEnsureWebhook_IsIdempotentByURL(t *testing.T) {
	f := &fakeWebhooks{}
	ctx := context.Background()

	first, _, err := forge.EnsureWebhook(ctx, f, repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if first != forge.WebhookCreated {
		t.Errorf("first outcome = %q, want created", first)
	}

	second, _, err := forge.EnsureWebhook(ctx, f, repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if second != forge.WebhookPresent {
		t.Errorf("second outcome = %q, want present", second)
	}
	if len(f.created) != 1 {
		t.Errorf("created %d hook(s), want exactly 1", len(f.created))
	}
}

// A trailing slash is the same endpoint: a config file edited by hand will have one and the
// forge's stored value will not, and treating those as different doubles the hook.
func TestEnsureWebhook_TrailingSlashIsTheSameHook(t *testing.T) {
	f := &fakeWebhooks{hooks: []forge.Webhook{
		{ID: 7, URL: spec.URL + "/", Events: spec.Events, Active: true},
	}}
	got, _, err := forge.EnsureWebhook(context.Background(), f, repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got != forge.WebhookPresent || len(f.created) != 0 {
		t.Errorf("outcome = %q, created %d; want present and nothing created", got, len(f.created))
	}
}

// An event set that moved is replaced rather than duplicated: the hook on that URL is the
// one the config describes, and neither forge offers a hook patch for both fields.
func TestEnsureWebhook_ReplacesWhenEventsMoved(t *testing.T) {
	f := &fakeWebhooks{hooks: []forge.Webhook{
		{ID: 7, URL: spec.URL, Events: []string{"push"}, Active: true},
	}}
	got, _, err := forge.EnsureWebhook(context.Background(), f, repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got != forge.WebhookReplaced {
		t.Errorf("outcome = %q, want replaced", got)
	}
	if len(f.deleted) != 1 || f.deleted[0] != 7 {
		t.Errorf("deleted = %v, want the stale hook", f.deleted)
	}
	if len(f.created) != 1 {
		t.Errorf("created = %d, want 1", len(f.created))
	}
}

// Removal matches on the URL only, so a run cannot delete a hook it was not told about.
//
// proven-by: TestRemoveWebhook_OnlyTouchesTheGivenURL
func TestRemoveWebhook_OnlyTouchesTheGivenURL(t *testing.T) {
	f := &fakeWebhooks{hooks: []forge.Webhook{
		{ID: 1, URL: spec.URL},
		{ID: 2, URL: "https://someone-else.example/hook"},
	}}
	n, err := forge.RemoveWebhook(context.Background(), f, repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	if len(f.hooks) != 1 || f.hooks[0].ID != 2 {
		t.Errorf("remaining = %v, want the other hook untouched", f.hooks)
	}
}

func TestEnsureWebhook_ListsBeforeCreating(t *testing.T) {
	f := &fakeWebhooks{failOn: "list"}
	if _, _, err := forge.EnsureWebhook(context.Background(), f, repo, spec); err == nil {
		t.Fatal("an error listing must be returned, not swallowed into a create")
	}
	if len(f.created) != 0 {
		t.Error("a create was attempted after the list failed")
	}
}

func TestSameEvents_IgnoresOrder(t *testing.T) {
	if !forge.SameEvents([]string{"a", "b"}, []string{"b", "a"}) {
		t.Error("order must not matter")
	}
	if forge.SameEvents([]string{"a"}, []string{"a", "b"}) {
		t.Error("a subset is not the same set")
	}
}

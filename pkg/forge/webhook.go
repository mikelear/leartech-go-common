package forge

import (
	"context"
	"strings"
)

// Webhook is one hook registered on a repository, as the forge reports it.
//
// Kind is the forge's own hook type: "web" on GitHub, "gitea" on Gitea/Forgejo. It is
// reported rather than assumed because the two forges reject each other's value.
type Webhook struct {
	ID          int64
	URL         string
	Kind        string
	Active      bool
	Events      []string
	ContentType string
}

// WebhookSpec is what a caller wants registered. Every field is a parameter: nothing here
// reads the environment, because who may see a secret is the caller's decision and not a
// library's.
type WebhookSpec struct {
	URL    string   // the endpoint the forge will call
	Secret string   // the HMAC secret the forge signs deliveries with; "" means unsigned
	Events []string // forge event names
	Active bool
}

// Webhooker is the hook surface a forge driver may implement.
//
// SEPARATE FROM Forge, NOT EMBEDDED IN IT. Registering a hook needs repo admin, which is a
// wider credential than the read one; and a separate interface keeps the test fakes written
// against Forge in other repos compiling when a driver gains this surface.
type Webhooker interface {
	// ListWebhooks returns every hook registered on the repo.
	ListWebhooks(ctx context.Context, repo Repo) ([]Webhook, error)

	// CreateWebhook registers spec and returns the created hook.
	CreateWebhook(ctx context.Context, repo Repo, spec WebhookSpec) (Webhook, error)

	// DeleteWebhook removes the hook with this forge-assigned id.
	DeleteWebhook(ctx context.Context, repo Repo, id int64) error
}

// WebhookOutcome is what EnsureWebhook did, so a caller can report a run rather than a
// binary "ok".
type WebhookOutcome string

const (
	// WebhookCreated means the hook was not there and now is.
	WebhookCreated WebhookOutcome = "created"
	// WebhookPresent means the hook was already registered and nothing changed.
	WebhookPresent WebhookOutcome = "present"
	// WebhookReplaced means a hook on that URL existed with a different event set or
	// content type, was removed, and was created again.
	WebhookReplaced WebhookOutcome = "replaced"
)

// SameURL compares two hook endpoints, ignoring a trailing slash and case in the host.
func SameURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}

// SameEvents reports whether two event sets are equal, order aside.
func SameEvents(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, e := range a {
		set[e] = struct{}{}
	}
	for _, e := range b {
		if _, ok := set[e]; !ok {
			return false
		}
	}
	return true
}

// EnsureWebhook makes the repo carry exactly one hook on spec.URL, creating it if it is
// absent and recreating it if it exists with a different event set.
//
// Idempotent by URL rather than by "was there an error": a forge answers 422 for a duplicate,
// which is not distinguishable from a real rejection without listing first.
// proven-by: TestEnsureWebhook_IsIdempotentByURL
func EnsureWebhook(ctx context.Context, w Webhooker, repo Repo, spec WebhookSpec) (WebhookOutcome, Webhook, error) {
	have, err := w.ListWebhooks(ctx, repo)
	if err != nil {
		return "", Webhook{}, err
	}
	for _, h := range have {
		if !SameURL(h.URL, spec.URL) {
			continue
		}
		if SameEvents(h.Events, spec.Events) && h.Active == spec.Active {
			return WebhookPresent, h, nil
		}
		// Events moved: the forge has no hook-patch on both of these forges, so replace.
		if err := w.DeleteWebhook(ctx, repo, h.ID); err != nil {
			return "", Webhook{}, err
		}
		created, cerr := w.CreateWebhook(ctx, repo, spec)
		return WebhookReplaced, created, cerr
	}
	created, cerr := w.CreateWebhook(ctx, repo, spec)
	return WebhookCreated, created, cerr
}

// RemoveWebhook deletes every hook on spec.URL and reports how many went. It is the inverse
// of EnsureWebhook and matches on the URL alone, so a hook on another endpoint is left alone.
// proven-by: TestRemoveWebhook_OnlyTouchesTheGivenURL
func RemoveWebhook(ctx context.Context, w Webhooker, repo Repo, spec WebhookSpec) (int, error) {
	have, err := w.ListWebhooks(ctx, repo)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, h := range have {
		if !SameURL(h.URL, spec.URL) {
			continue
		}
		if err := w.DeleteWebhook(ctx, repo, h.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

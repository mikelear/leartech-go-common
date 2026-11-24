package github

import (
	"context"
	"fmt"
	"net/http"

	"github.com/mikelear/leartech-go-common/pkg/forge"
)

// ghHook is GitHub's repo-hook object, the fields this driver reads and writes.
type ghHook struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	Events []string `json:"events"`
	Config struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
	} `json:"config"`
}

func (h ghHook) toForge() forge.Webhook {
	return forge.Webhook{
		ID:          h.ID,
		URL:         h.Config.URL,
		Kind:        h.Name,
		Active:      h.Active,
		Events:      h.Events,
		ContentType: h.Config.ContentType,
	}
}

// ListWebhooks returns every hook on the repo (GET /repos/{owner}/{repo}/hooks).
func (c *Client) ListWebhooks(ctx context.Context, repo forge.Repo) ([]forge.Webhook, error) {
	var hooks []ghHook
	path := fmt.Sprintf("/repos/%s/%s/hooks", repo.Owner, repo.Name)
	if err := c.do(ctx, http.MethodGet, path, nil, &hooks); err != nil {
		return nil, err
	}
	out := make([]forge.Webhook, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, h.toForge())
	}
	return out, nil
}

// CreateWebhook registers spec. GitHub's hook name is the constant "web", and a duplicate
// answer is 422, which the shared status mapping already reads as ErrConflict.
func (c *Client) CreateWebhook(ctx context.Context, repo forge.Repo, spec forge.WebhookSpec) (forge.Webhook, error) {
	config := map[string]any{"url": spec.URL, "content_type": "json"}
	if spec.Secret != "" {
		config["secret"] = spec.Secret
	}
	payload := map[string]any{
		"name":   "web",
		"active": spec.Active,
		"events": spec.Events,
		"config": config,
	}
	var created ghHook
	path := fmt.Sprintf("/repos/%s/%s/hooks", repo.Owner, repo.Name)
	if err := c.do(ctx, http.MethodPost, path, payload, &created); err != nil {
		return forge.Webhook{}, err
	}
	return created.toForge(), nil
}

// DeleteWebhook removes the hook by its GitHub-assigned id.
func (c *Client) DeleteWebhook(ctx context.Context, repo forge.Repo, id int64) error {
	path := fmt.Sprintf("/repos/%s/%s/hooks/%d", repo.Owner, repo.Name, id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

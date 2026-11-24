package gitea

import (
	"context"
	"fmt"
	"net/http"

	"github.com/mikelear/leartech-go-common/pkg/forge"
)

// gtHook is Gitea/Forgejo's repo-hook object, the fields this driver reads and writes.
type gtHook struct {
	ID     int64    `json:"id"`
	Type   string   `json:"type"`
	Active bool     `json:"active"`
	Events []string `json:"events"`
	Config struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
	} `json:"config"`
}

func (h gtHook) toForge() forge.Webhook {
	return forge.Webhook{
		ID:          h.ID,
		URL:         h.Config.URL,
		Kind:        h.Type,
		Active:      h.Active,
		Events:      h.Events,
		ContentType: h.Config.ContentType,
	}
}

// hookTypeForgejo is the hook type both forges accept: Forgejo keeps Gitea's hook-type names
// for compatibility, and answers "gitea". Sending "forgejo" is rejected by older instances.
const hookTypeForgejo = "gitea"

// ListWebhooks returns every hook on the repo (GET /repos/{owner}/{repo}/hooks under /api/v1).
func (c *Client) ListWebhooks(ctx context.Context, repo forge.Repo) ([]forge.Webhook, error) {
	var hooks []gtHook
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

// CreateWebhook registers spec. Gitea's hook type is required and is not "web"; a duplicate
// answer is 422, which the shared status mapping reads as ErrConflict.
func (c *Client) CreateWebhook(ctx context.Context, repo forge.Repo, spec forge.WebhookSpec) (forge.Webhook, error) {
	config := map[string]any{"url": spec.URL, "content_type": "json"}
	if spec.Secret != "" {
		config["secret"] = spec.Secret
	}
	payload := map[string]any{
		"type":   hookTypeForgejo,
		"active": spec.Active,
		"events": spec.Events,
		"config": config,
	}
	var created gtHook
	path := fmt.Sprintf("/repos/%s/%s/hooks", repo.Owner, repo.Name)
	if err := c.do(ctx, http.MethodPost, path, payload, &created); err != nil {
		return forge.Webhook{}, err
	}
	return created.toForge(), nil
}

// DeleteWebhook removes the hook by its Gitea/Forgejo-assigned id.
func (c *Client) DeleteWebhook(ctx context.Context, repo forge.Repo, id int64) error {
	path := fmt.Sprintf("/repos/%s/%s/hooks/%d", repo.Owner, repo.Name, id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

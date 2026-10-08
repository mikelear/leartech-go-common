package aigateway

import (
	"context"
	"net/http"
)

// ── web fetch: /v1/fetch ─────────────────────────────────────────────────────
//
// The gateway's URL-fetch capability behind the same client as chat and search.
// The gateway fetches behind its own allow-list and returns sanitized text, so
// a caller reaches pages the local machine's egress may block, debited to the
// key's budget the way a chat call is.

// FetchRequest is POST /v1/fetch.
type FetchRequest struct {
	URL string `json:"url"`
}

// FetchResult is the normalized fetch output. Content is sanitized text.
// FinalURL records where a redirect landed, so a caller can tell the page it
// got from the page it asked for.
type FetchResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Title       string `json:"title,omitempty"`
	Content     string `json:"content"`
	ContentType string `json:"content_type"`
	Truncated   bool   `json:"truncated"`
}

// Fetch retrieves one URL through the gateway. It debits the key's budget the
// same way Search does (one usage_event row, model=web_fetch).
//
// proven-by: TestFetch_ForwardsURLAndDecodesResult
// proven-by: TestFetch_UpstreamErrorIsAnErrorNotEmptyContent
func (c *Client) Fetch(ctx context.Context, req FetchRequest) (FetchResult, error) {
	var out FetchResult
	err := c.do(ctx, http.MethodPost, "/v1/fetch", req, &out)
	return out, err
}

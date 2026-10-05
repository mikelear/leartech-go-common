package aigateway

import (
	"context"
	"net/http"
)

// ── web search: /v1/search + /v1/websearch ──────────────────────────────────
//
// The gateway's search capability behind the same client as chat. Two lanes
// exist behind one wire: the caller names a provider the way it names a model,
// or omits the name and takes the cluster default. Discovery (/v1/websearch)
// is the /v1/models analogue for providers.

// SearchItem is one normalized result. Content is extracted page text when the
// provider supplies it (tavily does; brave gives a snippet; searxng gives the
// engine's snippet). Score is the provider's relevance if any, else 0.
type SearchItem struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// SearchResults is what every provider normalizes to — the only shape this
// client ever decodes. Provider names WHO ANSWERED, which is how a caller
// asserts a named request was honoured rather than silently falling back.
type SearchResults struct {
	Answer   string       `json:"answer,omitempty"`
	Items    []SearchItem `json:"results"`
	Provider string       `json:"provider"`
}

// SearchRequest is POST /v1/search. Provider empty = the cluster default.
type SearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
	Provider   string `json:"provider,omitempty"`
}

// Search runs one web search. It debits the key's budget exactly like a chat
// call (one usage_event row, model=web_search), so it is never free.
//
// proven-by: TestSearch_ForwardsProviderAndDecodesResults
func (c *Client) Search(ctx context.Context, req SearchRequest) (SearchResults, error) {
	var out SearchResults
	err := c.do(ctx, http.MethodPost, "/v1/search", req, &out)
	return out, err
}

// WebSearchProvider is one configured search provider.
type WebSearchProvider struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Default is the provider used when a SearchRequest omits Provider.
	Default bool `json:"default"`
	// Keyless reports whether this provider runs with no third-party
	// credential — the "open lane" (an in-cluster aggregator) vs the "curated
	// lane" (keyed vendors). A caller choosing on that axis reads this flag.
	Keyless bool `json:"keyless"`
}

// WebSearchDiscover is GET /v1/websearch — the /v1/models analogue for search
// providers: which exist, which is the default, which lane each is in.
//
// An older gateway without the endpoint answers 404; callers should treat
// that as "no discovery" and fall back to an unnamed Search, which is exactly
// what pre-provider-selection callers did.
//
// proven-by: TestWebSearchDiscovery_ListsProvidersAndDefault
func (c *Client) WebSearchDiscover(ctx context.Context) ([]WebSearchProvider, string, error) {
	var r struct {
		Object  string              `json:"object"`
		Data    []WebSearchProvider `json:"data"`
		Default string              `json:"default"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/websearch", nil, &r); err != nil {
		return nil, "", err
	}
	return r.Data, r.Default, nil
}

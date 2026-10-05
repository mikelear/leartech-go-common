package aigateway

import (
	"context"
	"net/http"
)

// ── gateway-hosted tools: /v1/tools ─────────────────────────────────────────
//
// The gateway publishes the tools IT hosts (today: search, fetch) and whether
// THIS credential may call each. A client concatenates them with its own local
// tools and whatever its MCP servers offer; it offers only the available ones
// to the model, and can show the unavailable ones (with the scope that would
// fix them) to the person at the prompt.

// ToolFunction is an OpenAI function-tool definition, verbatim — the client
// puts it straight back into `tools` on a chat request, so any other shape
// would need a translation layer with its own drift.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// Tool is one gateway-hosted tool.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
	// Available reports whether THIS credential may call it. The gateway
	// lists unavailable tools too, deliberately: a client can show "this
	// shell cannot search — it lacks gateway:web_search" instead of leaving
	// the question unanswerable.
	Available bool `json:"available"`
	// Scope names what a caller would need, so unavailable is actionable.
	Scope string `json:"scope,omitempty"`
}

type toolsResponse struct {
	Object string `json:"object"`
	Data   []Tool `json:"data"`
}

// Tools lists the tools this gateway hosts and whether this credential may
// call each. An older gateway without the endpoint answers 404; callers
// treat that as "no hosted tools" and carry on with local + MCP tools.
//
// proven-by: TestTools_ListsAvailabilityAndScope
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var r toolsResponse
	err := c.do(ctx, http.MethodGet, "/v1/tools", nil, &r)
	return r.Data, err
}

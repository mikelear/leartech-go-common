package aigateway

import (
	"context"
	"net/http"
	"testing"
)

// The contract a client assembles tool lists from: OpenAI function shape,
// per-credential availability, and the scope that would fix an unavailable
// tool — the actionable-closed-door shape the gateway publishes.
func TestTools_ListsAvailabilityAndScope(t *testing.T) {
	srv, _ := serveCapture(t, func(r *http.Request, _ []byte) (int, string) {
		if r.URL.Path != "/v1/tools" || r.Method != "GET" {
			return 404, `{}`
		}
		return 200, `{"object":"list","data":[
			{"type":"function","available":true,
			 "function":{"name":"search","description":"Search the web","parameters":{"type":"object"}}},
			{"type":"function","available":false,"scope":"leartechapi:gateway:web_fetch",
			 "function":{"name":"fetch","description":"Fetch a URL","parameters":{"type":"object"}}}]}`
	})
	c := New(srv.URL, testToken, srv.Client())

	tools, err := c.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools=%d want 2", len(tools))
	}
	if tools[0].Function.Name != "search" || !tools[0].Available {
		t.Errorf("search not available: %+v", tools[0])
	}
	if tools[1].Available || tools[1].Scope == "" {
		t.Errorf("fetch must be unavailable AND name its scope (the actionable shape): %+v", tools[1])
	}
}

// An older gateway 404s; that is "no hosted tools here", surfaced as an
// error the caller can branch on — not an empty list that reads as
// "the gateway hosts nothing", which a real 500 would also produce.
func TestTools_OldGateway404IsAnError(t *testing.T) {
	srv, _ := serveCapture(t, func(r *http.Request, _ []byte) (int, string) {
		return 404, `{"error":{"type":"not_found"}}`
	})
	c := New(srv.URL, testToken, srv.Client())
	if _, err := c.Tools(context.Background()); err == nil {
		t.Fatal("404 decoded without error — an older gateway would be misread as hosting no tools")
	}
}

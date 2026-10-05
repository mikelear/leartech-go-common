package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveCapture answers per-path, capturing the request body so the test can
// assert WHAT was forwarded, not only that a call happened.
func serveCapture(t *testing.T, handle func(r *http.Request, body []byte) (int, string)) (*httptest.Server, *captured) {
	t.Helper()
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.calls++
		got.authorization = r.Header.Get("Authorization")
		got.method = r.Method
		got.path = r.URL.Path
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		status, resp := handle(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// The request must carry the named provider, and the response must decode the
// normalized shape with Provider naming who answered — that field is how a
// caller detects a silent fallback (asked brave, got tavily).
func TestSearch_ForwardsProviderAndDecodesResults(t *testing.T) {
	var seen map[string]any
	srv, _ := serveCapture(t, func(r *http.Request, body []byte) (int, string) {
		_ = json.Unmarshal(body, &seen)
		if r.URL.Path != "/v1/search" || r.Method != http.MethodPost {
			return 404, `{}`
		}
		return 200, `{"answer":"a","results":[
			{"title":"T","url":"https://t.example","content":"c","score":0.9}],
			"provider":"brave"}`
	})
	c := New(srv.URL, testToken, srv.Client())

	res, err := c.Search(context.Background(), SearchRequest{Query: "q", MaxResults: 3, Provider: "brave"})
	if err != nil {
		t.Fatal(err)
	}
	if seen["provider"] != "brave" {
		t.Errorf("forwarded provider=%v, want brave — a dropped field silently changes lanes", seen["provider"])
	}
	if seen["max_results"] != float64(3) {
		t.Errorf("forwarded max_results=%v, want 3", seen["max_results"])
	}
	if res.Provider != "brave" {
		t.Errorf("decoded provider=%q, want brave — this field is the fallback detector", res.Provider)
	}
	if len(res.Items) != 1 || res.Items[0].Title != "T" || res.Items[0].Score != 0.9 {
		t.Errorf("items not decoded: %+v", res.Items)
	}
	if res.Answer != "a" {
		t.Errorf("answer=%q, want a", res.Answer)
	}
}

// An unnamed provider must NOT be sent as an empty string field the gateway
// could misread — omitempty keeps the lane choice with the cluster default.
func TestSearch_OmitsProviderWhenUnset(t *testing.T) {
	var raw map[string]any
	srv, _ := serveCapture(t, func(r *http.Request, body []byte) (int, string) {
		_ = json.Unmarshal(body, &raw)
		return 200, `{"results":[],"provider":"tavily"}`
	})
	c := New(srv.URL, testToken, srv.Client())

	if _, err := c.Search(context.Background(), SearchRequest{Query: "q"}); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["provider"]; present {
		t.Errorf("empty provider was forwarded as %q — omit it and let the default apply", raw["provider"])
	}
}

// Discovery: providers, the default, and the keyless flag — the axis a caller
// picks the open lane vs the curated lane on.
func TestWebSearchDiscovery_ListsProvidersAndDefault(t *testing.T) {
	srv, _ := serveCapture(t, func(r *http.Request, _ []byte) (int, string) {
		if r.URL.Path != "/v1/websearch" || r.Method != http.MethodGet {
			return 404, `{}`
		}
		return 200, `{"object":"list","default":"tavily","data":[
			{"id":"tavily","name":"Tavily","description":"keyed","default":true,"keyless":false},
			{"id":"searxng","name":"SearXNG","description":"aggregated","default":false,"keyless":true}]}`
	})
	c := New(srv.URL, testToken, srv.Client())

	provs, def, err := c.WebSearchDiscover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if def != "tavily" || len(provs) != 2 {
		t.Fatalf("default=%q providers=%d, want tavily/2", def, len(provs))
	}
	var sx *WebSearchProvider
	for i := range provs {
		if provs[i].ID == "searxng" {
			sx = &provs[i]
		}
	}
	if sx == nil || !sx.Keyless {
		t.Errorf("searxng missing or not keyless: %+v — the open-lane flag is the reason this endpoint exists", sx)
	}
}

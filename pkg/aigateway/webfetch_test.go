package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestFetch_ForwardsURLAndDecodesResult(t *testing.T) {
	var seen map[string]any
	srv, _ := serveCapture(t, func(r *http.Request, body []byte) (int, string) {
		_ = json.Unmarshal(body, &seen)
		if r.URL.Path != "/v1/fetch" || r.Method != http.MethodPost {
			return 404, `{}`
		}
		return 200, `{"url":"https://a.example","final_url":"https://a.example/x",
			"title":"T","content":"hello","content_type":"text/html","truncated":true}`
	})
	c := New(srv.URL, testToken, srv.Client())

	res, err := c.Fetch(context.Background(), FetchRequest{URL: "https://a.example"})
	if err != nil {
		t.Fatal(err)
	}
	if seen["url"] != "https://a.example" {
		t.Errorf("forwarded url=%v, want https://a.example", seen["url"])
	}
	if res.FinalURL != "https://a.example/x" {
		t.Errorf("decoded final_url=%q, want the redirect target", res.FinalURL)
	}
	if res.Title != "T" || res.Content != "hello" || res.ContentType != "text/html" {
		t.Errorf("fields not decoded: %+v", res)
	}
	if !res.Truncated {
		t.Errorf("truncated=false, want true")
	}
}

func TestFetch_UpstreamErrorIsAnErrorNotEmptyContent(t *testing.T) {
	srv, _ := serveCapture(t, func(r *http.Request, body []byte) (int, string) {
		return 400, `{"error":{"code":"fetch_error","message":"host not allowed"}}`
	})
	c := New(srv.URL, testToken, srv.Client())

	if _, err := c.Fetch(context.Background(), FetchRequest{URL: "https://blocked"}); err == nil {
		t.Fatal("a rejected fetch returned no error")
	}
}

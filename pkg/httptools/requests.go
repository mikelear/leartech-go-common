// Package httptools provides HTTP request helpers with auth token injection.
// Replaces spring-financial-group/mqa/pkg/httpTools/requests.
package httptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mikelear/leartech-go-common/pkg/auth"
)

var defaultClient = &http.Client{Timeout: 30 * time.Second}

// MakeAuthorisedGetRequest performs a GET with auth token and decodes the response.
func MakeAuthorisedGetRequest(tokenGetter auth.TokenGetter, ctx context.Context, url string, response interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building GET request: %w", err)
	}

	if err := tokenGetter.SetAuthHeader(ctx, req); err != nil {
		return fmt.Errorf("setting auth header: %w", err)
	}

	return doRequest(req, response)
}

// MakeAuthorisedPostRequest performs a POST with auth token, JSON body, and decodes the response.
func MakeAuthorisedPostRequest(tokenGetter auth.TokenGetter, ctx context.Context, url string, body interface{}, response interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshalling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("building POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := tokenGetter.SetAuthHeader(ctx, req); err != nil {
		return fmt.Errorf("setting auth header: %w", err)
	}

	return doRequest(req, response)
}

func doRequest(req *http.Request, response interface{}) error {
	resp, err := defaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", req.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("request to %s returned %d: %s", req.URL, resp.StatusCode, string(body))
	}

	if response != nil {
		if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
			return fmt.Errorf("decoding response from %s: %w", req.URL, err)
		}
	}

	return nil
}

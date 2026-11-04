// Package gitea is the Gitea/Forgejo driver for forge.Forge. Forgejo is API-
// compatible with Gitea, so one driver serves both.
package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/mikelear/leartech-go-common/pkg/forge"
)

// Client implements forge.Forge against the Gitea/Forgejo REST API (/api/v1).
type Client struct {
	base  string // instance base, e.g. https://forgejo.example.com
	token string
	http  *http.Client
}

// New returns a Gitea/Forgejo Forge presenting token at base, where base is the
// instance root (NOT including /api/v1 — the driver adds it). A nil http.Client
// uses http.DefaultClient.
func New(base, token string, h *http.Client) *Client {
	if h == nil {
		h = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(base, "/"), token: token, http: h}
}

func (c *Client) Kind() forge.Kind { return forge.KindGitea }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gitea: marshal body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, rdr)
	if err != nil {
		return fmt.Errorf("gitea: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		// Gitea/Forgejo use "token <token>" (not Bearer) for a personal token.
		req.Header.Set("Authorization", "token "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gitea: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if err := statusError(resp.StatusCode, raw); err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("gitea: decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func statusError(code int, body []byte) error {
	if code >= 200 && code < 300 {
		return nil
	}
	msg := strings.TrimSpace(string(body))
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", forge.ErrUnauthorized, msg)
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s", forge.ErrForbidden, msg)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", forge.ErrNotFound, msg)
	case http.StatusConflict, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", forge.ErrConflict, msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", forge.ErrRateLimited, msg)
	}
	if code >= 500 {
		return fmt.Errorf("%w: %d %s", forge.ErrUnavailable, code, msg)
	}
	return fmt.Errorf("gitea: unexpected status %d: %s", code, msg)
}

type gtComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (g gtComment) toForge() forge.Comment {
	return forge.Comment{ID: g.ID, Body: g.Body, HTMLURL: g.HTMLURL, AuthorLogin: g.User.Login}
}

func (c *Client) ListIssueComments(ctx context.Context, repo forge.Repo, number int) ([]forge.Comment, error) {
	var out []forge.Comment
	page := 1
	for {
		var batch []gtComment
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?limit=50&page=%d",
			repo.Owner, repo.Name, number, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		for _, b := range batch {
			out = append(out, b.toForge())
		}
		if len(batch) < 50 {
			return out, nil
		}
		page++
	}
}

func (c *Client) CreateIssueComment(ctx context.Context, repo forge.Repo, number int, body string) (forge.Comment, error) {
	var out gtComment
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", repo.Owner, repo.Name, number)
	if err := c.do(ctx, http.MethodPost, path, map[string]string{"body": body}, &out); err != nil {
		return forge.Comment{}, err
	}
	return out.toForge(), nil
}

func (c *Client) UpdateIssueComment(ctx context.Context, repo forge.Repo, commentID int64, body string) (forge.Comment, error) {
	var out gtComment
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%s", repo.Owner, repo.Name, strconv.FormatInt(commentID, 10))
	if err := c.do(ctx, http.MethodPatch, path, map[string]string{"body": body}, &out); err != nil {
		return forge.Comment{}, err
	}
	return out.toForge(), nil
}

type gtPull struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p gtPull) toForge() forge.PullRequest {
	return forge.PullRequest{
		Number: p.Number, Title: p.Title, Body: p.Body,
		HeadBranch: p.Head.Ref, BaseBranch: p.Base.Ref,
		State: p.State, Merged: p.Merged, HTMLURL: p.HTMLURL,
	}
}

func (c *Client) GetPullRequest(ctx context.Context, repo forge.Repo, number int) (forge.PullRequest, error) {
	var out gtPull
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", repo.Owner, repo.Name, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.PullRequest{}, err
	}
	return out.toForge(), nil
}

func (c *Client) FindOpenPRByHead(ctx context.Context, repo forge.Repo, head string) (forge.PullRequest, error) {
	// Gitea's /pulls has no head filter; list open and match client-side.
	var out []gtPull
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&limit=50", repo.Owner, repo.Name)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.PullRequest{}, err
	}
	for _, p := range out {
		if p.Head.Ref == head {
			return p.toForge(), nil
		}
	}
	return forge.PullRequest{}, fmt.Errorf("%w: no open PR for head %q", forge.ErrNotFound, head)
}

func (c *Client) EnsurePR(ctx context.Context, opt forge.OpenPROptions) (forge.PullRequest, error) {
	if pr, err := c.FindOpenPRByHead(ctx, opt.Repo, opt.Head); err == nil {
		return pr, nil // adopt
	}
	var out gtPull
	path := fmt.Sprintf("/repos/%s/%s/pulls", opt.Repo.Owner, opt.Repo.Name)
	body := map[string]string{"title": opt.Title, "head": opt.Head, "base": opt.Base, "body": opt.Body}
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return forge.PullRequest{}, err
	}
	return out.toForge(), nil
}

var _ forge.Forge = (*Client)(nil)

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

// Kind reports which forge REST shape this client speaks.
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
	defer func() { _ = resp.Body.Close() }()
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

// ListIssueComments implements forge.Forge.
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

// CreateIssueComment implements forge.Forge.
func (c *Client) CreateIssueComment(ctx context.Context, repo forge.Repo, number int, body string) (forge.Comment, error) {
	var out gtComment
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", repo.Owner, repo.Name, number)
	if err := c.do(ctx, http.MethodPost, path, map[string]string{"body": body}, &out); err != nil {
		return forge.Comment{}, err
	}
	return out.toForge(), nil
}

// UpdateIssueComment implements forge.Forge.
func (c *Client) UpdateIssueComment(ctx context.Context, repo forge.Repo, commentID int64, body string) (forge.Comment, error) {
	var out gtComment
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%s", repo.Owner, repo.Name, strconv.FormatInt(commentID, 10))
	if err := c.do(ctx, http.MethodPatch, path, map[string]string{"body": body}, &out); err != nil {
		return forge.Comment{}, err
	}
	return out.toForge(), nil
}

type gtPull struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Mergeable      bool   `json:"mergeable"`
	HTMLURL        string `json:"html_url"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (p gtPull) toForge() forge.PullRequest {
	labels := make([]string, 0, len(p.Labels))
	for _, l := range p.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	// Gitea reports mergeable as a bool, not GitHub's word. Map it onto the
	// shared Mergeable string so a caller reads one field across forges:
	// "clean" when mergeable, "dirty" when not.
	mergeable := ""
	if p.State == "open" {
		if p.Mergeable {
			mergeable = "clean"
		} else {
			mergeable = "dirty"
		}
	}
	return forge.PullRequest{
		Number: p.Number, Title: p.Title, Body: p.Body,
		HeadBranch: p.Head.Ref, BaseBranch: p.Base.Ref,
		HeadSHA: p.Head.SHA, BaseSHA: p.Base.SHA,
		State: p.State, Merged: p.Merged, MergedAt: p.MergedAt,
		MergeCommitSHA: p.MergeCommitSHA, Mergeable: mergeable,
		Labels: labels, HTMLURL: p.HTMLURL,
	}
}

// GetPullRequest implements forge.Forge.
func (c *Client) GetPullRequest(ctx context.Context, repo forge.Repo, number int) (forge.PullRequest, error) {
	var out gtPull
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", repo.Owner, repo.Name, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.PullRequest{}, err
	}
	return out.toForge(), nil
}

// FindOpenPRByHead implements forge.Forge.
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

// EnsurePR implements forge.Forge (adopt-or-create).
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

type gtFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// ChangedFiles implements forge.Forge.
func (c *Client) ChangedFiles(ctx context.Context, repo forge.Repo, number int) ([]forge.ChangedFile, error) {
	var out []forge.ChangedFile
	page := 1
	for {
		var batch []gtFile
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?limit=50&page=%d",
			repo.Owner, repo.Name, number, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		for _, f := range batch {
			out = append(out, forge.ChangedFile{Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions})
		}
		if len(batch) < 50 {
			return out, nil
		}
		page++
	}
}

// PullRequestDiff implements forge.Forge. Gitea/Forgejo serve the raw diff at
// /pulls/{n}.diff (no special Accept header needed).
func (c *Client) PullRequestDiff(ctx context.Context, repo forge.Repo, number int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d.diff", c.base, repo.Owner, repo.Name, number), nil)
	if err != nil {
		return "", fmt.Errorf("gitea: build diff request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gitea: get diff: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if err := statusError(resp.StatusCode, raw); err != nil {
		return "", err
	}
	return string(raw), nil
}

// FindPRByHead implements forge.Forge. Gitea has no head/state filter on /pulls,
// so it lists the requested state and matches head client-side.
func (c *Client) FindPRByHead(ctx context.Context, repo forge.Repo, head string, state forge.PRState) (forge.PullRequest, error) {
	var out []gtPull
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&limit=50", repo.Owner, repo.Name, state)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.PullRequest{}, err
	}
	for _, p := range out {
		if p.Head.Ref == head {
			return p.toForge(), nil
		}
	}
	return forge.PullRequest{}, fmt.Errorf("%w: no %s PR for head %q", forge.ErrNotFound, state, head)
}

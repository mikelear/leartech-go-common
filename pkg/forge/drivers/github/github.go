// Package github is the GitHub (api.github.com / GHE) driver for forge.Forge.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/mikelear/leartech-go-common/pkg/forge"
)

// DefaultBase is api.github.com. GitHub Enterprise passes its own base.
const DefaultBase = "https://api.github.com"

// Client implements forge.Forge against GitHub's REST v3 shape.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a GitHub Forge presenting token at base. Empty base means
// api.github.com; a nil http.Client uses http.DefaultClient.
func New(base, token string, h *http.Client) *Client {
	if base == "" {
		base = DefaultBase
	}
	if h == nil {
		h = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(base, "/"), token: token, http: h}
}

// Kind reports which forge REST shape this client speaks.
func (c *Client) Kind() forge.Kind { return forge.KindGitHub }

// do issues one request and maps the HTTP status onto a forge sentinel. out may
// be nil for requests whose body the caller ignores.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github: marshal body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		// GitHub accepts "Bearer <token>" and "token <token>"; Bearer is current.
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if err := statusError(resp.StatusCode, raw); err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("github: decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

// statusError maps an HTTP status onto a wrapped forge sentinel, carrying the
// forge's own message. 2xx returns nil.
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
	return fmt.Errorf("github: unexpected status %d: %s", code, msg)
}

type ghComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (g ghComment) toForge() forge.Comment {
	return forge.Comment{ID: g.ID, Body: g.Body, HTMLURL: g.HTMLURL, AuthorLogin: g.User.Login}
}

// ListIssueComments implements forge.Forge.
func (c *Client) ListIssueComments(ctx context.Context, repo forge.Repo, number int) ([]forge.Comment, error) {
	var out []forge.Comment
	page := 1
	for {
		var batch []ghComment
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d",
			repo.Owner, repo.Name, number, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		for _, b := range batch {
			out = append(out, b.toForge())
		}
		if len(batch) < 100 {
			return out, nil
		}
		page++
	}
}

// CreateIssueComment implements forge.Forge.
func (c *Client) CreateIssueComment(ctx context.Context, repo forge.Repo, number int, body string) (forge.Comment, error) {
	var out ghComment
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", repo.Owner, repo.Name, number)
	if err := c.do(ctx, http.MethodPost, path, map[string]string{"body": body}, &out); err != nil {
		return forge.Comment{}, err
	}
	return out.toForge(), nil
}

// UpdateIssueComment implements forge.Forge.
func (c *Client) UpdateIssueComment(ctx context.Context, repo forge.Repo, commentID int64, body string) (forge.Comment, error) {
	var out ghComment
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%s", repo.Owner, repo.Name, strconv.FormatInt(commentID, 10))
	if err := c.do(ctx, http.MethodPatch, path, map[string]string{"body": body}, &out); err != nil {
		return forge.Comment{}, err
	}
	return out.toForge(), nil
}

type ghPull struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Mergeable      string `json:"mergeable_state"`
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

func (p ghPull) toForge() forge.PullRequest {
	labels := make([]string, 0, len(p.Labels))
	for _, l := range p.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	return forge.PullRequest{
		Number: p.Number, Title: p.Title, Body: p.Body,
		HeadBranch: p.Head.Ref, BaseBranch: p.Base.Ref,
		HeadSHA: p.Head.SHA, BaseSHA: p.Base.SHA,
		State: p.State, Merged: p.Merged, MergedAt: p.MergedAt,
		MergeCommitSHA: p.MergeCommitSHA, Mergeable: p.Mergeable,
		Labels: labels, HTMLURL: p.HTMLURL,
	}
}

// GetPullRequest implements forge.Forge.
func (c *Client) GetPullRequest(ctx context.Context, repo forge.Repo, number int) (forge.PullRequest, error) {
	var out ghPull
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", repo.Owner, repo.Name, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.PullRequest{}, err
	}
	return out.toForge(), nil
}

// FindOpenPRByHead implements forge.Forge.
func (c *Client) FindOpenPRByHead(ctx context.Context, repo forge.Repo, head string) (forge.PullRequest, error) {
	var out []ghPull
	// GitHub's head filter wants owner:branch.
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s:%s&per_page=1",
		repo.Owner, repo.Name, repo.Owner, head)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.PullRequest{}, err
	}
	if len(out) == 0 {
		return forge.PullRequest{}, fmt.Errorf("%w: no open PR for head %q", forge.ErrNotFound, head)
	}
	return out[0].toForge(), nil
}

// EnsurePR implements forge.Forge (adopt-or-create).
func (c *Client) EnsurePR(ctx context.Context, opt forge.OpenPROptions) (forge.PullRequest, error) {
	if pr, err := c.FindOpenPRByHead(ctx, opt.Repo, opt.Head); err == nil {
		return pr, nil // adopt
	}
	var out ghPull
	path := fmt.Sprintf("/repos/%s/%s/pulls", opt.Repo.Owner, opt.Repo.Name)
	body := map[string]string{"title": opt.Title, "head": opt.Head, "base": opt.Base, "body": opt.Body}
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return forge.PullRequest{}, err
	}
	return out.toForge(), nil
}

var _ forge.Forge = (*Client)(nil)

type ghFile struct {
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
		var batch []ghFile
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d",
			repo.Owner, repo.Name, number, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		for _, f := range batch {
			out = append(out, forge.ChangedFile{Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions})
		}
		if len(batch) < 100 {
			return out, nil
		}
		page++
	}
}

// PullRequestDiff implements forge.Forge. It requests the raw unified diff via the
// GitHub v3.diff Accept header.
func (c *Client) PullRequestDiff(ctx context.Context, repo forge.Repo, number int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.base, repo.Owner, repo.Name, number), nil)
	if err != nil {
		return "", fmt.Errorf("github: build diff request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("github: get diff: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if err := statusError(resp.StatusCode, raw); err != nil {
		return "", err
	}
	return string(raw), nil
}

// FindPRByHead implements forge.Forge. StateAll surfaces a MERGED PR.
func (c *Client) FindPRByHead(ctx context.Context, repo forge.Repo, head string, state forge.PRState) (forge.PullRequest, error) {
	var out []ghPull
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&head=%s:%s&per_page=1",
		repo.Owner, repo.Name, state, repo.Owner, head)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.PullRequest{}, err
	}
	if len(out) == 0 {
		return forge.PullRequest{}, fmt.Errorf("%w: no %s PR for head %q", forge.ErrNotFound, state, head)
	}
	return out[0].toForge(), nil
}

type ghIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// GetIssue implements forge.Forge.
func (c *Client) GetIssue(ctx context.Context, repo forge.Repo, number int) (forge.Issue, error) {
	var out ghIssue
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", repo.Owner, repo.Name, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.Issue{}, err
	}
	labels := make([]string, 0, len(out.Labels))
	for _, l := range out.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	return forge.Issue{
		Number: out.Number, Title: out.Title, Body: out.Body,
		State: out.State, Labels: labels, URL: out.HTMLURL,
	}, nil
}

// FileAt implements forge.Forge.
func (c *Client) FileAt(ctx context.Context, repo forge.Repo, path, ref string) (forge.File, error) {
	var out struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	u := fmt.Sprintf("/repos/%s/%s/contents/%s", repo.Owner, repo.Name, strings.TrimPrefix(path, "/"))
	if ref != "" {
		u += "?ref=" + ref
	}
	if err := c.do(ctx, http.MethodGet, u, nil, &out); err != nil {
		return forge.File{}, err
	}
	// GitHub returns base64 with embedded newlines, and a decoder that does not
	// strip them fails on the first line break.
	raw := strings.ReplaceAll(out.Content, "\n", "")
	dec, derr := base64.StdEncoding.DecodeString(raw)
	if derr != nil {
		return forge.File{}, fmt.Errorf("github: decode contents of %s: %w", path, derr)
	}
	p := out.Path
	if p == "" {
		p = path
	}
	return forge.File{Path: p, Content: string(dec)}, nil
}

// HeadSHA implements forge.Forge.
func (c *Client) HeadSHA(ctx context.Context, repo forge.Repo, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	var out struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", repo.Owner, repo.Name, ref)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}
	if out.SHA == "" {
		return "", fmt.Errorf("%w: no commit at ref %q in %s", forge.ErrNotFound, ref, repo)
	}
	return out.SHA, nil
}

// CompareRefs implements forge.Forge.
func (c *Client) CompareRefs(ctx context.Context, repo forge.Repo, base, head string) (forge.Compare, error) {
	var out struct {
		AheadBy  int `json:"ahead_by"`
		BehindBy int `json:"behind_by"`
		Commits  []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		} `json:"commits"`
		Files []ghFile `json:"files"`
	}
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", repo.Owner, repo.Name, base, head)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return forge.Compare{}, err
	}
	cmp := forge.Compare{AheadBy: out.AheadBy, BehindBy: out.BehindBy}
	for _, cm := range out.Commits {
		cmp.Commits = append(cmp.Commits, forge.Commit{SHA: cm.SHA, Message: cm.Commit.Message})
	}
	for _, f := range out.Files {
		cmp.Files = append(cmp.Files, forge.ChangedFile{
			Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions,
		})
	}
	return cmp, nil
}

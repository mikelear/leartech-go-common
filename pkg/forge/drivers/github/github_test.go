package github_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/github"
)

func TestKind(t *testing.T) {
	if github.New("", "", nil).Kind() != forge.KindGitHub {
		t.Fatal("kind")
	}
}

func TestListIssueComments_Paginates(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth header = %q, want Bearer tok", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("page") == "1" {
			// 100 items → a second page is fetched
			w.Write([]byte("[" + repeatComment(100) + "]"))
			return
		}
		w.Write([]byte(`[{"id":999,"body":"last"}]`))
	}))
	defer srv.Close()

	c := github.New(srv.URL, "tok", srv.Client())
	out, err := c.ListIssueComments(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 pages, got %d calls", calls)
	}
	if len(out) != 101 || out[100].ID != 999 {
		t.Fatalf("pagination merge wrong: %d comments", len(out))
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		code int
		want error
	}{
		{401, forge.ErrUnauthorized},
		{403, forge.ErrForbidden},
		{404, forge.ErrNotFound},
		{409, forge.ErrConflict},
		{429, forge.ErrRateLimited},
		{503, forge.ErrUnavailable},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
			w.Write([]byte(`{"message":"from github"}`))
		}))
		c := github.New(srv.URL, "t", srv.Client())
		_, err := c.GetPullRequest(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1)
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: want %v, got %v", tc.code, tc.want, err)
		}
		if err == nil || !contains(err.Error(), "from github") {
			t.Errorf("status %d: forge message not surfaced: %v", tc.code, err)
		}
		srv.Close()
	}
}

func TestEnsurePR_AdoptsExistingOpenPR(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // FindOpenPRByHead
			w.Write([]byte(`[{"number":42,"state":"open","head":{"ref":"feat"},"base":{"ref":"main"}}]`))
			return
		}
		posted = true // a create POST would mean a duplicate
		w.WriteHeader(201)
		w.Write([]byte(`{"number":99}`))
	}))
	defer srv.Close()
	c := github.New(srv.URL, "t", srv.Client())
	pr, err := c.EnsurePR(context.Background(), forge.OpenPROptions{
		Repo: forge.Repo{Owner: "o", Name: "r"}, Head: "feat", Base: "main", Title: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if posted {
		t.Fatal("EnsurePR POSTed a new PR when one was already open — must adopt")
	}
	if pr.Number != 42 {
		t.Fatalf("adopted PR number = %d, want 42", pr.Number)
	}
}

func TestEnsurePR_CreatesWhenNoneOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`[]`)) // none open
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"number":7,"head":{"ref":"feat"},"base":{"ref":"main"}}`))
	}))
	defer srv.Close()
	c := github.New(srv.URL, "t", srv.Client())
	pr, err := c.EnsurePR(context.Background(), forge.OpenPROptions{
		Repo: forge.Repo{Owner: "o", Name: "r"}, Head: "feat", Base: "main", Title: "x",
	})
	if err != nil || pr.Number != 7 {
		t.Fatalf("create path: pr=%+v err=%v", pr, err)
	}
}

func repeatComment(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += `{"id":1,"body":"x"}`
	}
	return s
}
func contains(h, n string) bool { return len(h) >= len(n) && (indexOf(h, n) >= 0) }
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestCommentCRUD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`[{"id":11,"body":"hi","user":{"login":"bot"}}]`))
		case http.MethodPost:
			w.WriteHeader(201)
			w.Write([]byte(`{"id":12,"body":"new","html_url":"u"}`))
		case http.MethodPatch:
			w.Write([]byte(`{"id":11,"body":"edited"}`))
		}
	}))
	defer srv.Close()
	c := github.New(srv.URL, "t", srv.Client())
	repo := forge.Repo{Owner: "o", Name: "r"}

	list, err := c.ListIssueComments(context.Background(), repo, 1)
	if err != nil || len(list) != 1 || list[0].AuthorLogin != "bot" {
		t.Fatalf("list: %+v %v", list, err)
	}
	cr, err := c.CreateIssueComment(context.Background(), repo, 1, "new")
	if err != nil || cr.ID != 12 || cr.HTMLURL != "u" {
		t.Fatalf("create: %+v %v", cr, err)
	}
	up, err := c.UpdateIssueComment(context.Background(), repo, 11, "edited")
	if err != nil || up.Body != "edited" {
		t.Fatalf("update: %+v %v", up, err)
	}
}

func TestChangedFiles_Paginates(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") == "1" {
			out := "["
			for i := 0; i < 100; i++ {
				if i > 0 {
					out += ","
				}
				out += `{"filename":"f","status":"modified","additions":1,"deletions":0}`
			}
			w.Write([]byte(out + "]"))
			return
		}
		w.Write([]byte(`[{"filename":"last","status":"added","additions":3,"deletions":0}]`))
	}))
	defer srv.Close()
	c := github.New(srv.URL, "t", srv.Client())
	files, err := c.ChangedFiles(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1)
	if err != nil || calls != 2 || len(files) != 101 {
		t.Fatalf("files: calls=%d n=%d err=%v", calls, len(files), err)
	}
	if files[100].Path != "last" || files[100].Status != "added" {
		t.Fatalf("last file wrong: %+v", files[100])
	}
}

func TestPullRequestDiff_SendsDiffAcceptHeader(t *testing.T) {
	var accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		w.Write([]byte("diff --git a/x b/x\n"))
	}))
	defer srv.Close()
	c := github.New(srv.URL, "t", srv.Client())
	d, err := c.PullRequestDiff(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1)
	if err != nil || !contains(d, "diff --git") {
		t.Fatalf("diff: %q %v", d, err)
	}
	if accept != "application/vnd.github.v3.diff" {
		t.Fatalf("accept header = %q", accept)
	}
}

func TestFindPRByHead_AllSurfacesMerged(t *testing.T) {
	var gotState string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotState = r.URL.Query().Get("state")
		w.Write([]byte(`[{"number":3,"state":"closed","merged":true,"head":{"ref":"feat"}}]`))
	}))
	defer srv.Close()
	c := github.New(srv.URL, "t", srv.Client())
	pr, err := c.FindPRByHead(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "feat", forge.StateAll)
	if err != nil || !pr.Merged || gotState != "all" {
		t.Fatalf("find all: pr=%+v state=%q err=%v", pr, gotState, err)
	}
}

func TestGetPullRequest_RichFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"number": 5, "state": "closed", "merged": true,
			"merged_at": "2026-01-02T03:04:05Z", "merge_commit_sha": "abc123",
			"mergeable_state": "clean",
			"head": {"ref": "feat", "sha": "headsha"},
			"base": {"ref": "main", "sha": "basesha"},
			"labels": [{"name": "do-not-merge/hold"}, {"name": "size/M"}]
		}`))
	}))
	defer srv.Close()
	c := github.New(srv.URL, "t", srv.Client())
	pr, err := c.GetPullRequest(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.HeadSHA != "headsha" || pr.BaseSHA != "basesha" {
		t.Errorf("SHAs: head=%q base=%q", pr.HeadSHA, pr.BaseSHA)
	}
	if pr.MergedAt != "2026-01-02T03:04:05Z" || pr.MergeCommitSHA != "abc123" || pr.Mergeable != "clean" {
		t.Errorf("merge fields: %+v", pr)
	}
	if len(pr.Labels) != 2 || pr.Labels[0] != "do-not-merge/hold" {
		t.Errorf("labels: %v", pr.Labels)
	}
}

func TestGetIssue_SurfacesBodyLabelsAndState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/issues/12" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"number":12,"title":"why?","body":"the body","state":"open",
			"html_url":"u","labels":[{"name":"ba-question"},{"name":""}]}`))
	}))
	defer srv.Close()
	is, err := github.New(srv.URL, "t", srv.Client()).GetIssue(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if is.Title != "why?" || is.Body != "the body" || is.State != "open" || is.URL != "u" {
		t.Fatalf("%+v", is)
	}
	if len(is.Labels) != 1 || is.Labels[0] != "ba-question" {
		t.Fatalf("labels: %v", is.Labels)
	}
}

// GitHub returns contents base64-encoded WITH embedded newlines; a decoder that
// does not strip them fails on the first line break, which is why this asserts
// the decoded text rather than the status.
func TestFileAt_DecodesBase64WithNewlines(t *testing.T) {
	body := "line one\nline two\n"
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	// JSON cannot carry a raw newline inside a string, so the fixture escapes
	// it the way GitHub does on the wire (a literal backslash-n in the body).
	wrapped := enc[:8] + `\n` + enc[8:] + `\n`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "v1" {
			t.Errorf("ref = %q", r.URL.Query().Get("ref"))
		}
		w.Write([]byte(`{"path":"docs/a.md","encoding":"base64","content":"` + wrapped + `"}`))
	}))
	defer srv.Close()
	f, err := github.New(srv.URL, "t", srv.Client()).FileAt(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "docs/a.md", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if f.Content != body || f.Path != "docs/a.md" {
		t.Fatalf("file = %+v", f)
	}
}

func TestHeadSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/commits/main" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"sha":"abc123"}`))
	}))
	defer srv.Close()
	sha, err := github.New(srv.URL, "t", srv.Client()).HeadSHA(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "main")
	if err != nil || sha != "abc123" {
		t.Fatalf("%q %v", sha, err)
	}
}

func TestHeadSHA_EmptySHAIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	_, err := github.New(srv.URL, "t", srv.Client()).HeadSHA(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "main")
	if !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCompareRefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/compare/old...new" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"ahead_by":2,"behind_by":0,
			"commits":[{"sha":"c1","commit":{"message":"one"}},{"sha":"c2","commit":{"message":"two"}}],
			"files":[{"filename":"a.go","status":"modified","additions":3,"deletions":1}]}`))
	}))
	defer srv.Close()
	cmp, err := github.New(srv.URL, "t", srv.Client()).CompareRefs(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	if cmp.AheadBy != 2 || len(cmp.Commits) != 2 || cmp.Commits[1].Message != "two" {
		t.Fatalf("%+v", cmp)
	}
	if len(cmp.Files) != 1 || cmp.Files[0].Path != "a.go" {
		t.Fatalf("files: %+v", cmp.Files)
	}
}

func TestListOpenPRs_PaginatesAndMaps(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("state = %q, want open", r.URL.Query().Get("state"))
		}
		if r.URL.Query().Get("page") == "1" {
			full := make([]string, 100)
			for i := range full {
				full[i] = fmt.Sprintf(`{"number":%d,"title":"pr","head":{"ref":"b%d","sha":"s%d"},"base":{"ref":"main","sha":"b"}}`, i, i, i)
			}
			w.Write([]byte("[" + strings.Join(full, ",") + "]"))
			return
		}
		w.Write([]byte(`[{"number":42,"title":"last","html_url":"u","head":{"ref":"tail","sha":"ts"},"base":{"ref":"main","sha":"b"}}]`))
	}))
	defer srv.Close()

	c := github.New(srv.URL, "tok", srv.Client())
	out, err := c.ListOpenPRs(context.Background(), forge.Repo{Owner: "o", Name: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 pages, got %d", calls)
	}
	if len(out) != 101 || out[100].Number != 42 || out[100].HeadBranch != "tail" || out[100].HTMLURL != "u" {
		t.Fatalf("mapping wrong: %+v", out[100])
	}
}

func TestCommitStatuses_MapsGateContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/statuses/sha" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`[{"context":"Lighthouse Merge Status","state":"success","target_url":"http://l/merge/status","description":"Merge safe","updated_at":"2026-01-01T00:00:00Z"}]`))
	}))
	defer srv.Close()

	c := github.New(srv.URL, "tok", srv.Client())
	out, err := c.CommitStatuses(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Context != "Lighthouse Merge Status" || out[0].State != "success" {
		t.Fatalf("gate context did not map: %+v", out)
	}
}

func TestListIssueComments_MapsCreatedAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"id":1,"body":"b","created_at":"2026-01-02T03:04:05Z"}]`))
	}))
	defer srv.Close()
	out, err := github.New(srv.URL, "tok", srv.Client()).ListIssueComments(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].CreatedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("CreatedAt = %q", out[0].CreatedAt)
	}
}

func TestAdminSurface_UserAndRepoCreate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user" && r.Method == "GET":
			w.Write([]byte(`{"login":"the-owner"}`))
		case r.URL.Path == "/user/repos" && r.Method == "POST":
			w.Write([]byte(`{"full_name":"the-owner/new-repo"}`))
		case r.URL.Path == "/orgs/acme/repos" && r.Method == "POST":
			w.Write([]byte(`{"full_name":"acme/new-repo"}`))
		case r.URL.Path == "/repos/o/r/collaborators/bot" && r.Method == "PUT":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := github.New(srv.URL, "tok", srv.Client())
	who, err := c.AuthenticatedUser(context.Background())
	if err != nil || who != "the-owner" {
		t.Fatalf("who=%q err=%v", who, err)
	}
	repo, err := c.CreateUserRepo(context.Background(), "new-repo", "d", true, true)
	if err != nil || repo.Owner != "the-owner" || repo.Name != "new-repo" {
		t.Fatalf("repo=%+v err=%v", repo, err)
	}
	orgRepo, err := c.CreateOrgRepo(context.Background(), "acme", "new-repo", "d", true, true)
	if err != nil || orgRepo.Owner != "acme" {
		t.Fatalf("orgRepo=%+v err=%v", orgRepo, err)
	}
	if err := c.AddCollaborator(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "bot", "push"); err != nil {
		t.Fatal(err)
	}
}

func TestAdminSurface_CollaboratorBody(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	if err := github.New(srv.URL, "tok", srv.Client()).AddCollaborator(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "bot", "maintain"); err != nil {
		t.Fatal(err)
	}
	if got["permission"] != "maintain" {
		t.Fatalf("permission body = %v", got["permission"])
	}
}

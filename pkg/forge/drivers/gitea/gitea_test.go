package gitea_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/gitea"
)

func TestKind(t *testing.T) {
	if gitea.New("https://f", "", nil).Kind() != forge.KindGitea {
		t.Fatal("kind")
	}
}

func TestUsesApiV1PathAndTokenHeader(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"number":1,"head":{"ref":"x"},"base":{"ref":"main"}}`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "tok", srv.Client())
	if _, err := c.GetPullRequest(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotPath, "/api/v1/") {
		t.Fatalf("gitea path must be under /api/v1, got %q", gotPath)
	}
	if gotAuth != "token tok" {
		t.Fatalf("gitea auth header = %q, want 'token tok' (not Bearer)", gotAuth)
	}
}

func TestFindOpenPRByHead_MatchesClientSide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Gitea has no head filter — returns all open; driver matches locally.
		w.Write([]byte(`[{"number":3,"state":"open","head":{"ref":"other"},"base":{"ref":"main"}},
		                 {"number":8,"state":"open","head":{"ref":"feat"},"base":{"ref":"main"}}]`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	pr, err := c.FindOpenPRByHead(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "feat")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 8 {
		t.Fatalf("matched wrong PR: %d", pr.Number)
	}
}

func TestFindOpenPRByHead_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	_, err := c.FindOpenPRByHead(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "feat")
	if !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStatusMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"forgejo says no"}`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	_, err := c.GetPullRequest(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1)
	if !errors.Is(err, forge.ErrForbidden) || !strings.Contains(err.Error(), "forgejo says no") {
		t.Fatalf("want wrapped ErrForbidden with forge message, got %v", err)
	}
}

func TestCommentCRUD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`[{"id":11,"body":"hi","user":{"login":"bot"}}]`))
		case http.MethodPost:
			w.Write([]byte(`{"id":12,"body":"new"}`))
		case http.MethodPatch:
			w.Write([]byte(`{"id":11,"body":"edited"}`))
		}
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	repo := forge.Repo{Owner: "o", Name: "r"}

	list, err := c.ListIssueComments(context.Background(), repo, 1)
	if err != nil || len(list) != 1 || list[0].AuthorLogin != "bot" {
		t.Fatalf("list: %+v %v", list, err)
	}
	cr, err := c.CreateIssueComment(context.Background(), repo, 1, "new")
	if err != nil || cr.ID != 12 {
		t.Fatalf("create: %+v %v", cr, err)
	}
	up, err := c.UpdateIssueComment(context.Background(), repo, 11, "edited")
	if err != nil || up.Body != "edited" {
		t.Fatalf("update: %+v %v", up, err)
	}
}

func TestEnsurePR_AdoptAndCreate(t *testing.T) {
	// adopt
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`[{"number":5,"state":"open","head":{"ref":"feat"}}]`))
			return
		}
		t.Error("gitea EnsurePR POSTed despite open PR")
		w.Write([]byte(`{"number":99}`))
	}))
	c := gitea.New(srv.URL, "t", srv.Client())
	pr, err := c.EnsurePR(context.Background(), forge.OpenPROptions{Repo: forge.Repo{Owner: "o", Name: "r"}, Head: "feat", Base: "main"})
	if err != nil || pr.Number != 5 {
		t.Fatalf("adopt: %+v %v", pr, err)
	}
	srv.Close()

	// create
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"number":6,"head":{"ref":"feat"}}`))
	}))
	defer srv2.Close()
	c2 := gitea.New(srv2.URL, "t", srv2.Client())
	pr2, err := c2.EnsurePR(context.Background(), forge.OpenPROptions{Repo: forge.Repo{Owner: "o", Name: "r"}, Head: "feat", Base: "main"})
	if err != nil || pr2.Number != 6 {
		t.Fatalf("create: %+v %v", pr2, err)
	}
}

func TestListComments_Paginates(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") == "1" {
			items := "{" + `"id":1,"body":"x"` + "}"
			out := "["
			for i := 0; i < 50; i++ {
				if i > 0 {
					out += ","
				}
				out += items
			}
			out += "]"
			w.Write([]byte(out))
			return
		}
		w.Write([]byte(`[{"id":2,"body":"last"}]`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	out, err := c.ListIssueComments(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1)
	if err != nil || calls != 2 || len(out) != 51 {
		t.Fatalf("paginate: calls=%d n=%d err=%v", calls, len(out), err)
	}
}

func TestChangedFilesAndDiff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".diff") {
			w.Write([]byte("diff --git a/x b/x\n"))
			return
		}
		w.Write([]byte(`[{"filename":"f","status":"modified","additions":2,"deletions":1}]`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	files, err := c.ChangedFiles(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1)
	if err != nil || len(files) != 1 || files[0].Path != "f" {
		t.Fatalf("files: %+v %v", files, err)
	}
	d, err := c.PullRequestDiff(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 1)
	if err != nil || !strings.Contains(d, "diff --git") {
		t.Fatalf("diff: %q %v", d, err)
	}
}

func TestFindPRByHead_AllMatchesMerged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "all" {
			t.Errorf("state = %q, want all", r.URL.Query().Get("state"))
		}
		w.Write([]byte(`[{"number":4,"state":"closed","merged":true,"head":{"ref":"feat"}}]`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	pr, err := c.FindPRByHead(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "feat", forge.StateAll)
	if err != nil || !pr.Merged || pr.Number != 4 {
		t.Fatalf("find all: %+v %v", pr, err)
	}
}

func TestGetPullRequest_MapsMergeableBoolAndLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"number": 5, "state": "open", "mergeable": true,
			"head": {"ref": "feat", "sha": "h"}, "base": {"ref": "main", "sha": "b"},
			"labels": [{"name": "size/S"}]
		}`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	pr, err := c.GetPullRequest(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Mergeable != "clean" {
		t.Errorf("gitea mergeable bool should map to 'clean', got %q", pr.Mergeable)
	}
	if pr.HeadSHA != "h" || len(pr.Labels) != 1 || pr.Labels[0] != "size/S" {
		t.Errorf("rich fields: %+v", pr)
	}
}

func TestGetIssueAndFileAt(t *testing.T) {
	body := "hello\n"
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			w.Write([]byte(`{"path":"a.txt","encoding":"base64","content":"` + enc + `"}`))
			return
		}
		w.Write([]byte(`{"number":4,"title":"t","body":"b","state":"open","labels":[{"name":"x"}]}`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	is, err := c.GetIssue(context.Background(), forge.Repo{Owner: "o", Name: "r"}, 4)
	if err != nil || is.Title != "t" || len(is.Labels) != 1 {
		t.Fatalf("issue: %+v %v", is, err)
	}
	f, err := c.FileAt(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "a.txt", "")
	if err != nil || f.Content != body {
		t.Fatalf("file: %+v %v", f, err)
	}
}

func TestHeadSHAAndCompareRefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/compare/") {
			w.Write([]byte(`{"total_commits":1,"commits":[{"sha":"c1","commit":{"message":"m"}}],
				"files":[{"filename":"a","status":"added","additions":1,"deletions":0}]}`))
			return
		}
		w.Write([]byte(`[{"sha":"deadbeef"}]`))
	}))
	defer srv.Close()
	c := gitea.New(srv.URL, "t", srv.Client())
	sha, err := c.HeadSHA(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "main")
	if err != nil || sha != "deadbeef" {
		t.Fatalf("%q %v", sha, err)
	}
	cmp, err := c.CompareRefs(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "a", "b")
	if err != nil || cmp.AheadBy != 1 || len(cmp.Files) != 1 {
		t.Fatalf("%+v %v", cmp, err)
	}
}

func TestListOpenPRs_PaginatesAndMaps(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") == "1" {
			full := make([]string, 50)
			for i := range full {
				full[i] = fmt.Sprintf(`{"number":%d,"title":"pr","head":{"ref":"b%d","sha":"s%d"},"base":{"ref":"main","sha":"b"}}`, i, i, i)
			}
			w.Write([]byte("[" + strings.Join(full, ",") + "]"))
			return
		}
		w.Write([]byte(`[{"number":7,"title":"last","html_url":"u","head":{"ref":"tail","sha":"ts"},"base":{"ref":"main","sha":"b"}}]`))
	}))
	defer srv.Close()

	c := gitea.New(srv.URL, "tok", srv.Client())
	out, err := c.ListOpenPRs(context.Background(), forge.Repo{Owner: "o", Name: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 pages, got %d", calls)
	}
	if len(out) != 51 || out[50].Number != 7 || out[50].HeadBranch != "tail" {
		t.Fatalf("mapping wrong: %+v", out[50])
	}
}

func TestCommitStatuses_MapsGateContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/o/r/statuses/sha" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`[{"context":"Lighthouse Merge Status","status":"success","target_url":"http://l/merge/status","updated_at":"2026-01-01T00:00:00Z"}]`))
	}))
	defer srv.Close()

	c := gitea.New(srv.URL, "tok", srv.Client())
	out, err := c.CommitStatuses(context.Background(), forge.Repo{Owner: "o", Name: "r"}, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Context != "Lighthouse Merge Status" || out[0].State != "success" {
		t.Fatalf("gate context did not map: %+v", out)
	}
}

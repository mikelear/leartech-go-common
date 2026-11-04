package gitea_test

import (
	"context"
	"errors"
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
		switch {
		case r.Method == http.MethodGet:
			w.Write([]byte(`[{"id":11,"body":"hi","user":{"login":"bot"}}]`))
		case r.Method == http.MethodPost:
			w.Write([]byte(`{"id":12,"body":"new"}`))
		case r.Method == http.MethodPatch:
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

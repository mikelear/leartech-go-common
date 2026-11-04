package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

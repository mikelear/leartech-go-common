//go:build liveforge

// Live integration against a REAL Forgejo instance.
//
// The estate runs Forgejo at https://forgejo.jx.leartech.com (installed for
// exactly this: an unlimited, local forge to test against — no GitHub rate
// limits, no internet turn-around). These tests are opt-in behind the
// `liveforge` build tag so the deterministic gates never need the network:
//
//	go test -tags liveforge ./test/liveforge/... \
//	  -forge-base https://forgejo.jx.leartech.com \
//	  -forge-token "$(cat /tmp/forgejo-it-token)"
//
// They create their own throwaway repo under FORGEJO_IT_ORG (default
// it-test), mutate it through the driver, and delete it on cleanup. Nothing
// outside that org is touched.
package liveforge

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/gitea"
)

var (
	forgeBase  = flag.String("forge-base", os.Getenv("FORGE_BASE"), "Forgejo instance root")
	forgeToken = flag.String("forge-token", os.Getenv("FORGE_TOKEN"), "Forgejo API token")
	itOrg      = flag.String("forge-org", envOr("FORGEJO_IT_ORG", "it-test"), "throwaway org for test repos")
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func require(t *testing.T) (forge.Forge, string) {
	t.Helper()
	if *forgeBase == "" || *forgeToken == "" {
		t.Skip("liveforge: -forge-base / -forge-token not given (opt-in tier)")
	}
	return gitea.New(*forgeBase, *forgeToken, &http.Client{Timeout: 20 * time.Second}), *itOrg
}

func repoName() string { return fmt.Sprintf("live-%d", time.Now().UnixNano()%1e9) }

// createRepo provisions a throwaway repo via the instance API (the Forge
// interface is deliberately read/PR-shaped; provisioning is the test's job).
func createRepo(t *testing.T, f forge.Forge, name string) forge.Repo {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"auto_init":true,"default_branch":"main"}`, name)
	url := fmt.Sprintf("%s/api/v1/orgs/%s/repos", *forgeBase, *itOrg)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+*forgeToken)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil || resp.StatusCode >= 300 {
		t.Fatalf("create repo: %v %s", err, resp.Status)
	}
	_ = resp.Body.Close()
	return forge.Repo{Owner: *itOrg, Name: name}
}

// putFile commits one file onto a NEW branch via the contents API, which is
// how a driver-only rig creates a PR head branch without a git client.
func putFile(t *testing.T, repo forge.Repo, branch, path, content string) {
	t.Helper()
	body := fmt.Sprintf(`{"branch":%q,"new_branch":%q,"content":%q,"message":"live test file","author_email":"it@leartech.com","author_name":"liveforge"}`,
		"main", branch, base64.StdEncoding.EncodeToString([]byte(content)))
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s", *forgeBase, repo.Owner, repo.Name, path)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+*forgeToken)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("putFile on %s: %v %s %s", branch, err, resp.Status, b)
	}
	_ = resp.Body.Close()
}

func deleteRepo(t *testing.T, r forge.Repo) {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s", *forgeBase, r.Owner, r.Name)
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	req.Header.Set("Authorization", "token "+*forgeToken)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("cleanup repo: %v", err)
	}
	_ = resp.Body.Close()
}

func TestLive_HeadSHAAndFileAt(t *testing.T) {
	f, _ := require(t)
	r := repoName()
	repo := createRepo(t, f, r)
	defer deleteRepo(t, repo)

	sha, err := f.HeadSHA(context.Background(), repo, "main")
	if err != nil || len(sha) < 7 {
		t.Fatalf("HeadSHA: %v %q", err, sha)
	}
	file, err := f.FileAt(context.Background(), repo, "README.md", "main")
	if err != nil || len(file.Content) == 0 {
		t.Fatalf("FileAt: %v %+v", err, file)
	}
}

func TestLive_EnsurePRIsIdempotent(t *testing.T) {
	f, _ := require(t)
	repo := createRepo(t, f, repoName())
	defer deleteRepo(t, repo)

	putFile(t, repo, "live-branch", "LIVE.md", "live")
	opt := forge.OpenPROptions{Repo: repo, Head: "live-branch", Base: "main", Title: "live test", Body: "from liveforge"}
	pr1, err := f.EnsurePR(context.Background(), opt)
	if err != nil {
		t.Fatalf("EnsurePR create: %v", err)
	}
	pr2, err := f.EnsurePR(context.Background(), opt)
	if err != nil {
		t.Fatalf("EnsurePR adopt: %v", err)
	}
	if pr1.Number != pr2.Number {
		t.Fatalf("second EnsurePR opened a duplicate: %d vs %d", pr1.Number, pr2.Number)
	}
}

func TestLive_StickyUpsertEditsInPlace(t *testing.T) {
	f, _ := require(t)
	repo := createRepo(t, f, repoName())
	defer deleteRepo(t, repo)
	putFile(t, repo, "sticky-branch", "STICKY.md", "sticky")
	opt := forge.OpenPROptions{Repo: repo, Head: "sticky-branch", Base: "main", Title: "sticky", Body: ""}
	pr, err := f.EnsurePR(context.Background(), opt)
	if err != nil {
		t.Fatalf("EnsurePR: %v", err)
	}
	ctx := context.Background()
	c1, err := forge.UpsertSticky(ctx, f, repo, pr.Number, "live-sticky-key", "first body")
	if err != nil {
		t.Fatalf("UpsertSticky 1: %v", err)
	}
	c2, err := forge.UpsertSticky(ctx, f, repo, pr.Number, "live-sticky-key", "second body")
	if err != nil {
		t.Fatalf("UpsertSticky 2: %v", err)
	}
	if c1.ID != c2.ID {
		t.Fatalf("sticky upsert created a second comment: %d vs %d", c1.ID, c2.ID)
	}
	comments, err := f.ListIssueComments(ctx, repo, pr.Number)
	if err != nil {
		t.Fatalf("ListIssueComments: %v", err)
	}
	n := 0
	for _, c := range comments {
		if strings.Contains(c.Body, "live-sticky-key") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("sticky upsert left %d comments, want exactly 1", n)
	}
}

func TestLive_ListOpenPRsAndCommitStatuses(t *testing.T) {
	f, _ := require(t)
	repo := createRepo(t, f, repoName())
	defer deleteRepo(t, repo)
	ctx := context.Background()
	putFile(t, repo, "open-branch", "OPEN.md", "open")
	if _, err := f.EnsurePR(ctx, forge.OpenPROptions{Repo: repo, Head: "open-branch", Base: "main", Title: "open", Body: ""}); err != nil {
		t.Fatalf("EnsurePR: %v", err)
	}
	prs, err := f.ListOpenPRs(ctx, repo)
	if err != nil || len(prs) == 0 {
		t.Fatalf("ListOpenPRs: %v (%d)", err, len(prs))
	}
	sha, err := f.HeadSHA(ctx, repo, "main")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	sts, err := f.CommitStatuses(ctx, repo, sha)
	if err != nil {
		t.Fatalf("CommitStatuses on a statusless SHA: %v (must be an empty list, not an error)", err)
	}
	if len(sts) != 0 {
		t.Fatalf("expected no statuses, got %d", len(sts))
	}
}

package forge_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
)

// fakeForge is an in-memory Forge for exercising driver-independent logic
// (UpsertSticky). It records calls so tests assert create-vs-update.
type fakeForge struct {
	comments []forge.Comment
	nextID   int64
	created  int
	updated  int
	listErr  error
}

func (f *fakeForge) Kind() forge.Kind { return forge.KindGitHub }
func (f *fakeForge) ListIssueComments(_ context.Context, _ forge.Repo, _ int) ([]forge.Comment, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]forge.Comment(nil), f.comments...), nil
}
func (f *fakeForge) CreateIssueComment(_ context.Context, _ forge.Repo, _ int, body string) (forge.Comment, error) {
	f.nextID++
	f.created++
	c := forge.Comment{ID: f.nextID, Body: body}
	f.comments = append(f.comments, c)
	return c, nil
}
func (f *fakeForge) UpdateIssueComment(_ context.Context, _ forge.Repo, id int64, body string) (forge.Comment, error) {
	f.updated++
	for i := range f.comments {
		if f.comments[i].ID == id {
			f.comments[i].Body = body
			return f.comments[i], nil
		}
	}
	return forge.Comment{}, forge.ErrNotFound
}
func (f *fakeForge) ListOpenPRs(context.Context, forge.Repo) ([]forge.PullRequest, error) {
	return nil, nil
}

func (f *fakeForge) CommitStatuses(context.Context, forge.Repo, string) ([]forge.CommitStatus, error) {
	return nil, nil
}

func (f *fakeForge) GetPullRequest(context.Context, forge.Repo, int) (forge.PullRequest, error) {
	return forge.PullRequest{}, nil
}
func (f *fakeForge) FindOpenPRByHead(context.Context, forge.Repo, string) (forge.PullRequest, error) {
	return forge.PullRequest{}, forge.ErrNotFound
}
func (f *fakeForge) FindPRByHead(context.Context, forge.Repo, string, forge.PRState) (forge.PullRequest, error) {
	return forge.PullRequest{}, forge.ErrNotFound
}
func (f *fakeForge) ChangedFiles(context.Context, forge.Repo, int) ([]forge.ChangedFile, error) {
	return nil, nil
}
func (f *fakeForge) PullRequestDiff(context.Context, forge.Repo, int) (string, error) {
	return "", nil
}
func (f *fakeForge) GetIssue(context.Context, forge.Repo, int) (forge.Issue, error) {
	return forge.Issue{}, forge.ErrNotFound
}
func (f *fakeForge) FileAt(context.Context, forge.Repo, string, string) (forge.File, error) {
	return forge.File{}, forge.ErrNotFound
}
func (f *fakeForge) HeadSHA(context.Context, forge.Repo, string) (string, error) {
	return "", forge.ErrNotFound
}
func (f *fakeForge) CompareRefs(context.Context, forge.Repo, string, string) (forge.Compare, error) {
	return forge.Compare{}, forge.ErrNotFound
}
func (f *fakeForge) EnsurePR(context.Context, forge.OpenPROptions) (forge.PullRequest, error) {
	return forge.PullRequest{}, nil
}

func TestUpsertSticky_CreatesWhenAbsentThenUpdatesInPlace(t *testing.T) {
	f := &fakeForge{}
	repo := forge.Repo{Owner: "o", Name: "r"}

	first, err := forge.UpsertSticky(context.Background(), f, repo, 7, "coverage", "82%")
	if err != nil {
		t.Fatal(err)
	}
	if f.created != 1 || f.updated != 0 {
		t.Fatalf("first upsert: created=%d updated=%d, want 1/0", f.created, f.updated)
	}
	if !strings.Contains(first.Body, forge.StickyMarker("coverage")) || !strings.Contains(first.Body, "82%") {
		t.Fatalf("body missing marker or content: %q", first.Body)
	}

	second, err := forge.UpsertSticky(context.Background(), f, repo, 7, "coverage", "91%")
	if err != nil {
		t.Fatal(err)
	}
	if f.created != 1 || f.updated != 1 {
		t.Fatalf("second upsert: created=%d updated=%d, want 1/1 (update in place)", f.created, f.updated)
	}
	if second.ID != first.ID {
		t.Fatalf("second upsert changed id %d -> %d; sticky must update in place", first.ID, second.ID)
	}
	if len(f.comments) != 1 {
		t.Fatalf("want 1 comment after two upserts, got %d (duplicate sticky)", len(f.comments))
	}
}

func TestUpsertSticky_DistinctKeysAreIndependent(t *testing.T) {
	f := &fakeForge{}
	repo := forge.Repo{Owner: "o", Name: "r"}
	_, _ = forge.UpsertSticky(context.Background(), f, repo, 1, "coverage", "a")
	_, _ = forge.UpsertSticky(context.Background(), f, repo, 1, "ai-review", "b")
	if len(f.comments) != 2 {
		t.Fatalf("two keys should be two comments, got %d", len(f.comments))
	}
}

func TestUpsertSticky_WrapsListError(t *testing.T) {
	f := &fakeForge{listErr: forge.ErrUnavailable}
	_, err := forge.UpsertSticky(context.Background(), f, forge.Repo{Owner: "o", Name: "r"}, 1, "k", "v")
	if !errors.Is(err, forge.ErrUnavailable) {
		t.Fatalf("want wrapped ErrUnavailable, got %v", err)
	}
}

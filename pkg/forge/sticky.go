package forge

import (
	"context"
	"fmt"
	"strings"
)

// StickyMarker is the invisible HTML comment that identifies a sticky comment so
// a re-run updates it in place instead of posting a new one. The key names the
// logical comment (e.g. "coverage", "ai-review", "run-report"); two different
// keys on the same PR are two independent stickies.
//
// It is an HTML comment so it renders invisibly in both GitHub's and Forgejo's
// markdown. The recorder, the catalog and ai-review all used an ad-hoc marker of
// this shape; this is the one definition they now share.
func StickyMarker(key string) string {
	return fmt.Sprintf("<!-- leartech-sticky: %s -->", key)
}

// UpsertSticky posts body as a sticky comment on repo#number, or edits the
// existing one carrying the same marker key. The marker is prepended to body, so
// callers pass only their content.
//
// This is the single implementation of the upsert the whole estate was
// duplicating (recorder app/report/comment.go, the catalog's end2end /
// rust-test / dotnet-mutants curl blocks, ai-review's sticky verdict). It reads
// the comment list, matches on the marker, and PATCHes or POSTs accordingly.
func UpsertSticky(ctx context.Context, f Forge, repo Repo, number int, key, body string) (Comment, error) {
	marker := StickyMarker(key)
	full := marker + "\n" + body

	existing, err := f.ListIssueComments(ctx, repo, number)
	if err != nil {
		return Comment{}, fmt.Errorf("sticky %q: list comments: %w", key, err)
	}
	for _, c := range existing {
		if strings.Contains(c.Body, marker) {
			updated, err := f.UpdateIssueComment(ctx, repo, c.ID, full)
			if err != nil {
				return Comment{}, fmt.Errorf("sticky %q: update #%d: %w", key, c.ID, err)
			}
			return updated, nil
		}
	}
	created, err := f.CreateIssueComment(ctx, repo, number, full)
	if err != nil {
		return Comment{}, fmt.Errorf("sticky %q: create: %w", key, err)
	}
	return created, nil
}

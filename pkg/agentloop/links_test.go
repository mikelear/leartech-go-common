package agentloop

import (
	"reflect"
	"testing"
)

func TestPullRequestLinks_FindsAPullRequestURL(t *testing.T) {
	got := PullRequestLinks(
		"https://github.com/mikelear/leartech-ba-service/pull/76\n")
	want := []string{"https://github.com/mikelear/leartech-ba-service/pull/76"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestPullRequestLinks_FindsAnIssue(t *testing.T) {
	if got := PullRequestLinks("see https://github.com/o/r/issues/12"); len(got) != 1 {
		t.Errorf("got %v, want the issue", got)
	}
}

// Everything else stays hidden, or the one worth opening is buried.
func TestPullRequestLinks_IgnoresOrdinaryURLs(t *testing.T) {
	for _, s := range []string{
		"https://github.com/mikelear/leartech-ba-service",
		"https://example.com/pull/1",
		"https://github.com/o/r/commit/abc123",
		"https://raw.githubusercontent.com/o/r/main/docs/rules.md",
	} {
		if got := PullRequestLinks(s); len(got) != 0 {
			t.Errorf("PullRequestLinks(%q) = %v, want none", s, got)
		}
	}
}

func TestPullRequestLinks_DeduplicatesAndKeepsOrder(t *testing.T) {
	got := PullRequestLinks(`https://github.com/o/r/pull/2 then
	  https://github.com/o/r/pull/1 and https://github.com/o/r/pull/2 again`)
	want := []string{"https://github.com/o/r/pull/2", "https://github.com/o/r/pull/1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestPullRequestLinks_NothingInPlainOutput(t *testing.T) {
	if got := PullRequestLinks("ran 3 tests, all passed"); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

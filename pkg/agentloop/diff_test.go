package agentloop

import (
	"fmt"
	"strings"
	"testing"
)

func TestUnifiedDiff_IdenticalContentIsNoChange(t *testing.T) {
	if d := unifiedDiff("a\nb\n", "a\nb\n"); d != "" {
		t.Errorf("diff = %q, want empty for identical content", d)
	}
}

func TestUnifiedDiff_ShowsAddedAndRemovedLines(t *testing.T) {
	d := unifiedDiff("one\ntwo\nthree\n", "one\nTWO\nthree\n")
	if !strings.Contains(d, "- two") {
		t.Errorf("diff = %q, want the old line marked removed", d)
	}
	if !strings.Contains(d, "+ TWO") {
		t.Errorf("diff = %q, want the new line marked added", d)
	}
	if !strings.Contains(d, "1 line(s) added, 1 removed") {
		t.Errorf("diff = %q, want a summary", d)
	}
}

// EVERY changed line appears.
//
// A diff that omits a change is worse than no diff: it is read as
// complete, so the omitted change is the one that gets approved
// unseen.
func TestUnifiedDiff_ShowsEveryChangedLine(t *testing.T) {
	var oldLines, newLines []string
	changed := map[int]bool{3: true, 17: true, 40: true}
	for i := 0; i < 60; i++ {
		oldLines = append(oldLines, fmt.Sprintf("line %d", i))
		if changed[i] {
			newLines = append(newLines, fmt.Sprintf("CHANGED %d", i))
			continue
		}
		newLines = append(newLines, fmt.Sprintf("line %d", i))
	}
	d := unifiedDiff(strings.Join(oldLines, "\n"), strings.Join(newLines, "\n"))
	for i := range changed {
		if !strings.Contains(d, fmt.Sprintf("+ CHANGED %d", i)) {
			t.Errorf("the change at line %d is missing from the diff:\n%s", i, d)
		}
		if !strings.Contains(d, fmt.Sprintf("- line %d", i)) {
			t.Errorf("the removal at line %d is missing from the diff:\n%s", i, d)
		}
	}
}

// Unchanged regions far from a change are collapsed.
//
// Otherwise a one-line edit to a long file prints the whole file and
// the change is a needle in it.
func TestUnifiedDiff_CollapsesUnchangedRegions(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	old := strings.Join(lines, "\n")
	lines[100] = "CHANGED"
	d := unifiedDiff(old, strings.Join(lines, "\n"))

	if strings.Contains(d, "line 5") {
		t.Errorf("a line far from the change was printed:\n%s", d)
	}
	if !strings.Contains(d, "+ CHANGED") {
		t.Errorf("the change is missing:\n%s", d)
	}
	if !strings.Contains(d, "…") {
		t.Errorf("the collapse was not marked:\n%s", d)
	}
}

// A diff too large to read says so, and says why that is a reason to
// refuse.
func TestUnifiedDiff_SaysWhenItTruncated(t *testing.T) {
	var a, b []string
	for i := 0; i < 400; i++ {
		a = append(a, fmt.Sprintf("old %d", i))
		b = append(b, fmt.Sprintf("new %d", i))
	}
	d := unifiedDiff(strings.Join(a, "\n"), strings.Join(b, "\n"))
	if !strings.Contains(d, "not shown") {
		t.Errorf("a truncated diff did not admit it:\n%s", d[max(0, len(d)-200):])
	}
	if !strings.Contains(d, "git") {
		t.Error("the truncation notice should point somewhere better than this prompt")
	}
	if n := strings.Count(d, "\n"); n > maxDiffLines+6 {
		t.Errorf("the diff printed %d lines, want it bounded near %d", n, maxDiffLines)
	}
}

// A new file shows as all additions.
func TestUnifiedDiff_ANewFileIsAllAdditions(t *testing.T) {
	d := unifiedDiff("", "hello\nworld\n")
	if !strings.Contains(d, "+ hello") || !strings.Contains(d, "+ world") {
		t.Errorf("diff = %q, want every line added", d)
	}
	if strings.Contains(d, "- ") {
		t.Errorf("diff = %q, want no removals for a new file", d)
	}
}

// Deleting every line shows as all removals.
func TestUnifiedDiff_EmptyingAFileIsAllRemovals(t *testing.T) {
	d := unifiedDiff("hello\nworld\n", "")
	if !strings.Contains(d, "- hello") || !strings.Contains(d, "- world") {
		t.Errorf("diff = %q, want every line removed", d)
	}
}

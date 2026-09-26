package agentloop

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines surround each change.
const diffContext = 3

// maxDiffLines bounds what an approval prompt shows.
//
// A different decision from the script, which is never cut. // proven-by: TestUnifiedDiff_SaysWhenItTruncated
// A script is small enough to read whole; a diff runs to thousands of
// lines, and a prompt nobody reads approves anything. The cut is stated
// loudly, and a diff that had to be cut is itself the signal — a change
// that large is one to look at with git rather than approve from here.
const maxDiffLines = 120

// unifiedDiff renders the change from old to new, with context.
//
// LINE BASED AND DELIBERATELY SIMPLE. A character-level diff reads
// better for prose and worse for code, and the thing being approved here
// is code. The only property that matters is that every changed line
// appears — a diff that omits a change is worse than no diff at all,
// because it is read as complete.
//
// proven-by: TestUnifiedDiff_ShowsAddedAndRemovedLines
// proven-by: TestUnifiedDiff_ShowsEveryChangedLine
// proven-by: TestUnifiedDiff_SaysWhenItTruncated
// proven-by: TestUnifiedDiff_IdenticalContentIsNoChange
func unifiedDiff(old, new string) string {
	if old == new {
		return ""
	}
	a, b := splitLines(old), splitLines(new)
	common := lcs(a, b)

	var lines []string
	i, j := 0, 0
	for _, c := range common {
		for i < len(a) && a[i] != c {
			lines = append(lines, "- "+a[i])
			i++
		}
		for j < len(b) && b[j] != c {
			lines = append(lines, "+ "+b[j])
			j++
		}
		lines = append(lines, "  "+c)
		i++
		j++
	}
	for ; i < len(a); i++ {
		lines = append(lines, "- "+a[i])
	}
	for ; j < len(b); j++ {
		lines = append(lines, "+ "+b[j])
	}

	return render(trimContext(lines))
}

// trimContext drops runs of unchanged lines far from any change.
//
// Without it a one-line edit to a long file shows the whole file, and
// the change is a needle in it.
func trimContext(lines []string) []string {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if strings.HasPrefix(l, "  ") {
			continue
		}
		for d := -diffContext; d <= diffContext; d++ {
			if i+d >= 0 && i+d < len(lines) {
				keep[i+d] = true
			}
		}
	}
	var out []string
	skipping := false
	for i, l := range lines {
		if keep[i] {
			if skipping {
				out = append(out, "  …")
				skipping = false
			}
			out = append(out, l)
			continue
		}
		skipping = true
	}
	return out
}

func render(lines []string) string {
	added, removed := 0, 0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+ "):
			added++
		case strings.HasPrefix(l, "- "):
			removed++
		}
	}

	shown := lines
	cut := 0
	if len(shown) > maxDiffLines {
		cut = len(shown) - maxDiffLines
		shown = shown[:maxDiffLines]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d line(s) added, %d removed\n", added, removed)
	for _, l := range shown {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if cut > 0 {
		// STATED LOUDLY, and phrased as a reason to refuse. A cut diff
		// is not a smaller diff; it is an unread one.
		fmt.Fprintf(&b, "\n… %d more diff line(s) not shown. A change this large "+
			"is one to read with git, not to approve from a prompt.\n", cut)
	}
	return strings.TrimRight(b.String(), "\n")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lcs returns the longest common subsequence of two line slices.
//
// O(n*m) in memory, which is why the caller bounds the input: this runs
// on files a person is about to approve, not on a repository.
func lcs(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			table[i][j] = max(table[i+1][j], table[i][j+1])
		}
	}
	var out []string
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

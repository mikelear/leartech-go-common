package agentloop_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/agentloop"
)

// searchTree writes a small REAL repository-shaped tree. Real files on a
// real filesystem: every defect these tools have had lived in the boundary
// between a path and the disk, and an in-memory fixture has no boundary.
func searchTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"main.go":                 "package main\n\nfunc main() { doThing() }\n",
		"internal/thing/thing.go": "package thing\n\n// doThing does it\nfunc doThing() {}\n",
		"internal/thing/other.go": "package thing\n\nfunc Other() { DOTHING() }\n",
		"docs/notes.md":           "doThing is described here\n",
		".git/config":             "[remote]\n  url = https://x:secret@github.com/a/b\n  doThing\n",
		"vendor/dep/dep.go":       "package dep\n\nfunc doThing() {}\n",
		"node_modules/p/index.js": "function doThing(){}\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func search(t *testing.T, root, args string) (string, error) {
	t.Helper()
	return agentloop.Search(root).Run(json.RawMessage(args))
}

// A MATCH NAMES ITS FILE AND LINE.
//
// file:line: text is what a caller needs to go straight to it, and it is
// what `grep -rn` produced in the runs this tool replaces.
func TestSearch_FindsMatchesWithFileAndLine(t *testing.T) {
	root := searchTree(t)

	out, err := search(t, root, `{"pattern":"doThing"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "internal/thing/thing.go:4:") {
		t.Errorf("no file:line for the definition:\n%s", out)
	}
	if !strings.Contains(out, "main.go:3:") {
		t.Errorf("no file:line for the call site:\n%s", out)
	}
}

// .git IS NEVER SEARCHED.
//
// It is repository internals nobody is looking for, and in some clones its
// config holds a credential — which a search would then copy into the next
// prompt and into Loki.
func TestSearch_SkipsTheGitDirectory(t *testing.T) {
	root := searchTree(t)

	out, err := search(t, root, `{"pattern":"doThing"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if strings.Contains(out, ".git") {
		t.Errorf("searched .git:\n%s", out)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("a credential from .git/config reached the result:\n%s", out)
	}
}

// BUILD OUTPUT AND DEPENDENCIES ARE NOT THE PROJECT.
//
// They match source patterns freely and would exhaust the cap before any
// source was reached — which is why a shell grep gets `| head` appended.
func TestSearch_SkipsVendorAndBuildOutput(t *testing.T) {
	root := searchTree(t)

	out, err := search(t, root, `{"pattern":"doThing"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, unwanted := range []string{"vendor/", "node_modules/"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("searched %s:\n%s", unwanted, out)
		}
	}
}

// TRUNCATION IS ANNOUNCED.
//
// A silently capped answer is indistinguishable from a complete one, and a
// model reasons about what is absent.
func TestSearch_SaysWhenItTruncated(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "needle %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := search(t, root, `{"pattern":"needle"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "stopped at") {
		t.Errorf("a truncated result did not say so:\n%s", out[:min(400, len(out))])
	}
}

// A SUBDIRECTORY NARROWS THE WALK.
func TestSearch_RestrictsToASubdirectory(t *testing.T) {
	root := searchTree(t)

	out, err := search(t, root, `{"pattern":"doThing","path":"internal/thing"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if strings.Contains(out, "main.go") || strings.Contains(out, "docs/") {
		t.Errorf("searched outside the given path:\n%s", out)
	}
	if !strings.Contains(out, "thing.go") {
		t.Errorf("did not search inside the given path:\n%s", out)
	}
}

func TestSearch_FiltersBySuffix(t *testing.T) {
	root := searchTree(t)

	out, err := search(t, root, `{"pattern":"doThing","suffix":".md"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "docs/notes.md") {
		t.Errorf("the suffix filter excluded the file it should match:\n%s", out)
	}
	if strings.Contains(out, ".go:") {
		t.Errorf("the suffix filter let .go through:\n%s", out)
	}
}

func TestSearch_IgnoreCase(t *testing.T) {
	root := searchTree(t)

	out, err := search(t, root, `{"pattern":"dothing","ignore_case":true}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "other.go") {
		t.Errorf("case-insensitive search missed DOTHING:\n%s", out)
	}
}

// NO MATCH IS A FINDING, NOT A FAILURE.
//
// An error would make a model retry a question that has already been
// answered.
func TestSearch_NoMatchIsNotAnError(t *testing.T) {
	root := searchTree(t)

	out, err := search(t, root, `{"pattern":"thisAppearsNowhere"}`)
	if err != nil {
		t.Fatalf("no match returned an error: %v", err)
	}
	if !strings.Contains(out, "no match") {
		t.Errorf("no match did not say so: %q", out)
	}
}

// THE SAME BOUNDARY AS EVERY OTHER PATH TOOL.
func TestSearch_RefusesToEscapeTheRoot(t *testing.T) {
	root := searchTree(t)

	if _, err := search(t, root, `{"pattern":"x","path":"../.."}`); err == nil {
		t.Fatal("a search escaped the working directory")
	}
}

func TestSearch_AnInvalidPatternSaysSo(t *testing.T) {
	root := searchTree(t)

	_, err := search(t, root, `{"pattern":"("}`)
	if err == nil {
		t.Fatal("an invalid regular expression was accepted")
	}
	if !strings.Contains(err.Error(), "regular expression") {
		t.Errorf("the error does not say the pattern was the problem: %v", err)
	}
}

func TestSearch_PatternIsRequired(t *testing.T) {
	root := searchTree(t)

	if _, err := search(t, root, `{}`); err == nil {
		t.Error("an empty pattern was accepted")
	}
}

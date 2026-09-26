package agentloop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCall(t *testing.T, root, path, content string) (string, error) {
	t.Helper()
	args, err := json.Marshal(writeArgs{Path: path, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return WriteFile(root).Run(args)
}

func preview(t *testing.T, root, path, content string) string {
	t.Helper()
	args, err := json.Marshal(writeArgs{Path: path, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return WriteFile(root).Preview(args)
}

func TestWriteFile_WritesUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := writeCall(t, root, "notes.md", "hello\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello\n" {
		t.Errorf("contents = %q, want what was written", b)
	}
}

// The root holds for writes as it does for reads.
func TestWriteFile_RefusesToEscapeTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../victim.txt", "../../etc/hosts", "a/../../escape.txt"} {
		if _, err := writeCall(t, root, p, "overwritten"); err == nil {
			t.Errorf("%s was written outside the root", p)
		}
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "original\n" {
		t.Error("a file outside the root was modified")
	}
}

// A symlinked directory does not become a way out.
func TestWriteFile_RefusesASymlinkPointingOut(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := writeCall(t, root, "escape/planted.txt", "x"); err == nil {
		t.Error("a write reached through a symlink out of the root")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Error("the file was created outside the root")
	}
}

// .git is refused for writing as it is for reading.
//
// Writing .git/config is a way to change where a push goes.
func TestWriteFile_RefusesGitInternals(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git/config", ".git/hooks/pre-commit"} {
		if _, err := writeCall(t, root, p, "x"); err == nil {
			t.Errorf("%s was written", p)
		}
	}
}

// A new file in a new directory works, and the preview says a directory
// will be created.
func TestWriteFile_CreatesMissingParentDirectories(t *testing.T) {
	root := t.TempDir()
	if p := preview(t, root, "pkg/sub/new.go", "package sub\n"); !strings.Contains(p, "creates directory") {
		t.Errorf("preview = %q, want the directory creation named", p)
	}
	if _, err := writeCall(t, root, "pkg/sub/new.go", "package sub\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pkg", "sub", "new.go")); err != nil {
		t.Errorf("the file was not created: %v", err)
	}
}

func TestWriteFile_RefusesADirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCall(t, root, "adir", "x"); err == nil {
		t.Error("a directory was written as a file")
	}
}

func TestWriteFile_RefusesContentTooLargeToApprove(t *testing.T) {
	root := t.TempDir()
	_, err := writeCall(t, root, "big.txt", strings.Repeat("x", maxWriteBytes+1))
	if err == nil {
		t.Fatal("an unreviewable write was accepted")
	}
	if !strings.Contains(err.Error(), "reviewable") {
		t.Errorf("err = %v, want the reason to be reviewability", err)
	}
}

// The preview is a diff against what is there now.
func TestWritePreview_ShowsADiffAgainstTheCurrentFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := preview(t, root, "f.txt", "one\nTWO\n")
	if !strings.Contains(p, "- two") || !strings.Contains(p, "+ TWO") {
		t.Errorf("preview = %q, want a diff of the change", p)
	}
	if strings.Contains(p, "NEW FILE") {
		t.Errorf("preview = %q, want it to know the file exists", p)
	}
}

func TestWritePreview_ANewFileSaysItIsNew(t *testing.T) {
	p := preview(t, t.TempDir(), "brand-new.md", "hello\nworld\n")
	if !strings.Contains(p, "NEW FILE") {
		t.Errorf("preview = %q, want it marked new", p)
	}
	if !strings.Contains(p, "+ hello") {
		t.Errorf("preview = %q, want the content shown", p)
	}
}

// A write that will be refused says so BEFORE approval.
//
// Approving something that then fails teaches nothing about what was
// wrong, and trains the reader to approve without reading.
func TestWritePreview_ARefusalIsVisibleBeforeApproval(t *testing.T) {
	p := preview(t, t.TempDir(), "../escape.txt", "x")
	if !strings.Contains(p, "cannot be written") {
		t.Errorf("preview = %q, want the refusal visible at approval time", p)
	}
}

// Writing identical content is reported as no change.
func TestWritePreview_IdenticalContentIsNoChange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("same\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := preview(t, root, "f.txt", "same\n"); !strings.Contains(p, "no change") {
		t.Errorf("preview = %q, want it to say nothing would change", p)
	}
}

// A path that does not exist yet is still contained.
func TestResolveForWrite_ContainsAPathThatDoesNotExistYet(t *testing.T) {
	root := t.TempDir()
	got, err := resolveForWrite(root, "deep/nested/new.txt")
	if err != nil {
		t.Fatalf("a new nested path was refused: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		real, _ := filepath.EvalSymlinks(root)
		if !strings.HasPrefix(got, real) {
			t.Errorf("resolved to %q, which is outside %q", got, root)
		}
	}
}

// The tool declares itself as writing, which is what the gate judges.
func TestWriteFile_DeclaresItselfAsWriting(t *testing.T) {
	if e := WriteFile(t.TempDir()).Effect; e != Writes {
		t.Errorf("effect = %q, want %q", e, Writes)
	}
}

// The registry asks the tool to render its own preview.
func TestPreviewOf_UsesTheToolsOwnRenderer(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry()
	if err := r.Add(WriteFile(root)); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(writeArgs{Path: "x.txt", Content: "hello\n"})
	got := r.PreviewOf("local__write_file", args)
	if !strings.Contains(got, "NEW FILE") {
		t.Errorf("preview = %q, want the tool's own rendering", got)
	}
	if strings.Contains(got, `{"path"`) {
		t.Errorf("preview = %q, want the diff rather than the raw arguments", got)
	}
}

// A tool with no renderer shows its arguments.
func TestPreviewOf_FallsBackToTheArguments(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(Tool{Name: "local__plain", Effect: Reads,
		Run: func(json.RawMessage) (string, error) { return "", nil }}); err != nil {
		t.Fatal(err)
	}
	if got := r.PreviewOf("local__plain", json.RawMessage(`{"a":1}`)); got != `{"a":1}` {
		t.Errorf("preview = %q, want the arguments", got)
	}
	if got := r.PreviewOf("local__unknown", json.RawMessage(`{"a":1}`)); got != `{"a":1}` {
		t.Errorf("unknown tool preview = %q, want the arguments", got)
	}
}

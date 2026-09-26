package agentloop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func args(t *testing.T, path string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A duplicate name is refused, not shadowed.
//
// Last-registration-wins is a precedence rule by another name, and the tool
// that loses vanishes with no signal. Three sources will feed this registry
// — local, MCP and the gateway's own — so a collision is a question for a
// human, not something to resolve quietly.
func TestRegistry_RefusesADuplicateRatherThanShadowing(t *testing.T) {
	r := NewRegistry()
	first := Tool{Name: "local__read_file", Effect: Reads, Run: func(json.RawMessage) (string, error) {
		return "first", nil
	}}
	second := Tool{Name: "local__read_file", Effect: Reads, Run: func(json.RawMessage) (string, error) {
		return "second", nil
	}}

	if err := r.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(second); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("err = %v, want ErrDuplicateTool", err)
	}
	// And the FIRST survives: a refused registration must not have
	// half-replaced the incumbent.
	out, ok := r.Run("local__read_file", nil)
	if !ok || out != "first" {
		t.Errorf("run = %q ok=%v, want the original tool intact", out, ok)
	}
}

// An unknown tool is reported to the model, not swallowed.
//
// A model that hallucinates a name must be told so its next turn can
// correct. Silence reads to the model as a tool that returned nothing.
func TestRegistry_AnUnknownToolIsReportedToTheModel(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Tool{Name: "local__read_file", Effect: Reads, Run: func(json.RawMessage) (string, error) {
		return "", nil
	}})

	out, ok := r.Run("local__write_file", nil)
	if ok {
		t.Fatal("an unknown tool reported success")
	}
	if !strings.Contains(out, "local__write_file") {
		t.Errorf("result %q does not name what was asked for", out)
	}
	// The available set travels, so the model can correct in one turn
	// rather than guessing again.
	if !strings.Contains(out, "local__read_file") {
		t.Errorf("result %q does not list what IS available", out)
	}
}

// A failing tool hands its reason to the model.
//
// A tool failing is information the model can act on — a missing file may
// mean it should look elsewhere. A failure the model cannot see is one it
// repeats.
func TestRegistry_AFailureReachesTheModelAsText(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Tool{Name: "t", Effect: Reads, Run: func(json.RawMessage) (string, error) {
		return "", errors.New("no such file: notes.md")
	}})

	out, ok := r.Run("t", nil)
	if ok {
		t.Fatal("a failing tool reported success")
	}
	if !strings.Contains(out, "notes.md") {
		t.Errorf("result %q loses the reason", out)
	}
}

// The ordinary case works.
func TestReadFile_ReadsAFileUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFile(root).Run(args(t, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// Escaping the root is refused.
//
// THE ONE THAT MATTERS. The model chooses the argument. A shell that can
// read any path is a shell that can read ~/.ssh/id_rsa and send it to a
// supplier, on a turn the user approved without reading closely.
func TestReadFile_RefusesToEscapeTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, path := range []string{
		"../outside.txt",
		"../../etc/hosts",
		"./../../etc/hosts",
		outside, // an absolute path outside
	} {
		got, err := ReadFile(root).Run(args(t, path))
		if err == nil {
			t.Errorf("reading %q was allowed and returned %q", path, got)
		}
		if got == "secret" {
			t.Fatalf("reading %q returned the file outside the root", path)
		}
	}
}

// A symlink pointing out is refused too.
//
// filepath.Clean removes "../" TEXTUALLY, so a check on the cleaned path
// accepts a symlink whose target escapes. The resolution has to happen
// before the comparison.
func TestReadFile_RefusesASymlinkPointingOut(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := ReadFile(root).Run(args(t, "innocent.txt"))
	if err == nil {
		t.Fatalf("a symlink out of the root was followed and returned %q", got)
	}
	if got == "secret" {
		t.Fatal("the symlink target was read")
	}
}

// A sibling directory sharing a prefix is not inside the root.
//
// A string-prefix check alone accepts /work-secrets as being under /work.
func TestReadFile_APrefixSiblingIsNotInside(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "work")
	sibling := filepath.Join(base, "work-secrets")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sibling, "k.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := ReadFile(root).Run(args(t, "../work-secrets/k.txt")); err == nil {
		t.Errorf("a prefix-sharing sibling was treated as inside the root: %q", got)
	}
}

// A directory and a missing file each say which they are.
func TestReadFile_NamesWhatWentWrong(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadFile(root).Run(args(t, "sub")); err == nil ||
		!strings.Contains(err.Error(), "directory") {
		t.Errorf("reading a directory gave %v, want it named", err)
	}
	if _, err := ReadFile(root).Run(args(t, "absent.md")); err == nil ||
		!strings.Contains(err.Error(), "no such file") {
		t.Errorf("reading a missing file gave %v, want it named", err)
	}
}

// A file too large to send is refused rather than truncated.
//
// The contents go into the next prompt and are paid for by the token.
// Truncating silently hands the model a partial file it believes is whole.
func TestReadFile_RefusesAFileTooLargeToSend(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, 300*1024)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFile(root).Run(args(t, "big.bin"))
	if err == nil {
		t.Fatalf("a 300KB file was returned (%d bytes)", len(got))
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("err = %v, want the limit explained", err)
	}
}

// Malformed arguments are reported, not guessed at.
func TestReadFile_MalformedArgumentsAreRefused(t *testing.T) {
	root := t.TempDir()
	if _, err := ReadFile(root).Run(json.RawMessage(`{"path":`)); err == nil {
		t.Error("truncated JSON arguments were accepted")
	}
	if _, err := ReadFile(root).Run(args(t, "")); err == nil {
		t.Error("an empty path was accepted")
	}
}

func mustAdd(t *testing.T, r *Registry, tools ...Tool) {
	t.Helper()
	for _, tl := range tools {
		if err := r.Add(tl); err != nil {
			t.Fatal(err)
		}
	}
}

func runTool(t *testing.T, tl Tool, args string) (string, error) {
	t.Helper()
	return tl.Run(json.RawMessage(args))
}

// The listing names entries and says which are directories.
func TestListDir_ListsEntriesAndMarksDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o750); err != nil {
		t.Fatal(err)
	}

	out, err := runTool(t, ListDir(root), `{"path":"."}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "notes.md") {
		t.Errorf("out = %q, want the file", out)
	}
	if !strings.Contains(out, "docs/") {
		t.Errorf("out = %q, want the directory marked with a slash", out)
	}
}

// The root is not a suggestion here either.
func TestListDir_RefusesToEscapeTheRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := runTool(t, ListDir(root), `{"path":"../.."}`); err == nil {
		t.Fatal("listing escaped the working directory")
	}
}

// A symlink is resolved before the comparison, not after.
func TestListDir_RefusesASymlinkPointingOut(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := runTool(t, ListDir(root), `{"path":"escape"}`); err == nil {
		t.Fatal("a symlinked directory escaped the working directory")
	}
}

// Pointed at a file, it says so and names the tool that does read files.
//
// An error that only says "not a directory" makes the model guess again.
func TestListDir_AFileIsNotADirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runTool(t, ListDir(root), `{"path":"notes.md"}`)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "local__read_file") {
		t.Errorf("err = %v, want it to name the tool that reads files", err)
	}
}

// A truncated listing SAYS it was truncated.
//
// The dangerous failure is silent: a model shown 300 of 400 files concludes
// the other 100 do not exist.
func TestListDir_SaysWhenItTruncated(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxEntries+20; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runTool(t, ListDir(root), `{"path":"."}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not listed") {
		t.Errorf("out did not admit truncation: %q", out[max(0, len(out)-200):])
	}
}

// An empty directory says so rather than returning nothing.
func TestListDir_AnEmptyDirectorySaysSo(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, ListDir(root), `{"path":"empty"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Error("an empty string teaches the model nothing; it will try again")
	}
}

// A pattern with no separator matches base names at any depth.
func TestGlob_FindsByBaseNameAtAnyDepth(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "deep"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"top.md", "docs/mid.md", "docs/deep/low.md", "other.txt"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runTool(t, Glob(root), `{"pattern":"*.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"top.md", "mid.md", "low.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("out = %q, want it to contain %s", out, want)
		}
	}
	if strings.Contains(out, "other.txt") {
		t.Errorf("out = %q, want only the pattern's matches", out)
	}
}

// A pattern WITH a separator is anchored to the relative path.
func TestGlob_APatternWithASeparatorMatchesTheRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "deep"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"docs/mid.md", "docs/deep/low.md"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runTool(t, Glob(root), `{"pattern":"docs/*.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mid.md") {
		t.Errorf("out = %q, want the direct child", out)
	}
	if strings.Contains(out, "low.md") {
		t.Errorf("out = %q, want the deeper file excluded by the anchor", out)
	}
}

// A pattern reaching upward is refused with a reason.
func TestGlob_RefusesToEscapeTheRoot(t *testing.T) {
	root := t.TempDir()
	_, err := runTool(t, Glob(root), `{"pattern":"../*.md"}`)
	if err == nil {
		t.Fatal("want a refusal")
	}
	if !strings.Contains(err.Error(), "working directory") {
		t.Errorf("err = %v, want it to say why", err)
	}
}

// .git is skipped, or it exhausts the cap before reaching real files.
func TestGlob_SkipsTheGitDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "objects", "cafe.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, Glob(root), `{"pattern":"*.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "cafe.md") {
		t.Errorf("out = %q, want .git skipped", out)
	}
	if !strings.Contains(out, "real.md") {
		t.Errorf("out = %q, want the real file", out)
	}
}

// No matches is an answer, not an empty string.
func TestGlob_SaysWhenNothingMatched(t *testing.T) {
	root := t.TempDir()
	out, err := runTool(t, Glob(root), `{"pattern":"*.nope"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing matched") {
		t.Errorf("out = %q, want an explicit answer", out)
	}
}

// A capped result admits there are more.
func TestGlob_SaysWhenItTruncated(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxMatches+10; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.md", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runTool(t, Glob(root), `{"pattern":"*.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "there are more") {
		t.Errorf("out did not admit truncation: %q", out[max(0, len(out)-120):])
	}
}

// All three tools coexist under distinct qualified names.
func TestRegistry_TheThreeLocalToolsRegisterTogether(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry()
	mustAdd(t, r, ReadFile(root), ListDir(root), Glob(root))
	want := []string{"local__read_file", "local__list_dir", "local__glob"}
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("names = %v, want %v", got, want)
		}
	}
}

// .git is refused, because the model picks the path.
//
// A clone made with a token URL has https://user:token@github.com/... in
// .git/config, and a tool that reads it puts that credential into the
// next prompt — and into the transcript, and into the supplier's logs.
func TestReadFile_RefusesGitInternals(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs", "heads"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git/config", ".git/HEAD", ".git/refs/heads/main"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{".git/config", ".git/HEAD", ".git/refs/heads/main"} {
		_, err := runTool(t, ReadFile(root), `{"path":"`+p+`"}`)
		if err == nil {
			t.Errorf("%s was read; .git is refused", p)
			continue
		}
		if !strings.Contains(err.Error(), ".git") {
			t.Errorf("err for %s = %v, want it to say why", p, err)
		}
	}
}

// Listing .git is refused too, or the model simply reads what it finds.
func TestListDir_RefusesTheGitDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git", ".git/refs"} {
		if _, err := runTool(t, ListDir(root), `{"path":"`+p+`"}`); err == nil {
			t.Errorf("%s was listed; .git is refused", p)
		}
	}
}

// A file merely NAMED something git-ish is still an ordinary file.
//
// The guard: matching on a substring would refuse .gitignore, .github/
// and a directory called gitlab — all of which are project files someone
// has a real reason to read.
func TestResolveUnder_AFileMerelyNamedGitIsFine(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "gitlab"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".gitignore", ".github/workflows/ci.yml", "gitlab/notes.md", "git"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.WriteFile(full, []byte("real content"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := runTool(t, ReadFile(root), `{"path":"`+p+`"}`)
		if err != nil {
			t.Errorf("%s was refused: %v", p, err)
			continue
		}
		if out != "real content" {
			t.Errorf("%s = %q, want its contents", p, out)
		}
	}
}

// A failing tool's output survives alongside its error.
//
// THE PART WORTH READING IS USUALLY THE OUTPUT. bash returns its
// stderr with a non-zero exit; an MCP tool returns the server's own
// message with IsError. Keeping only the wrapper turned a real
// explanation into "reported an error" — and a model told nothing it
// can act on retries the same call.
func TestRegistry_AFailingToolKeepsItsOutput(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(Tool{Name: "local__thing", Effect: Reads,
		Run: func(json.RawMessage) (string, error) {
			return "deepwiki: repository not indexed", errors.New("thing reported an error")
		}}); err != nil {
		t.Fatal(err)
	}
	out, ok := r.Run("local__thing", nil)
	if ok {
		t.Fatal("a failing tool reported success")
	}
	if !strings.Contains(out, "repository not indexed") {
		t.Errorf("out = %q, want the tool's own output kept", out)
	}
	if !strings.Contains(out, "thing reported an error") {
		t.Errorf("out = %q, want the error kept too", out)
	}

	// With no output, the error alone is still returned.
	if err := r.Add(Tool{Name: "local__silent", Effect: Reads,
		Run: func(json.RawMessage) (string, error) { return "", errors.New("just the error") }}); err != nil {
		t.Fatal(err)
	}
	quiet, _ := r.Run("local__silent", nil)
	if quiet != "just the error" {
		t.Errorf("out = %q, want the bare error with no padding", quiet)
	}
}

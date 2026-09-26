package agentloop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bashOut(t *testing.T, root, script string, timeout time.Duration) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"script": script})
	if err != nil {
		t.Fatal(err)
	}
	return Bash(root, timeout).Run(args)
}

func TestBash_RunsAScriptAndReturnsItsOutput(t *testing.T) {
	out, err := bashOut(t, t.TempDir(), "echo hello from the script", 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "hello from the script") {
		t.Errorf("out = %q, want the script's output", out)
	}
}

// It runs in the shell's root, not wherever the process happens to be.
//
// A model asking to "list the files here" means the working directory it
// was told about, and running somewhere else would answer a different
// question convincingly.
func TestBash_RunsInTheRootNotTheProcessDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := bashOut(t, root, "ls", 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "marker.txt") {
		t.Errorf("out = %q, want the root's contents", out)
	}
}

// A failing script returns its output AND its status.
//
// Discarding the output on failure tells the model only that something
// went wrong, and its next move is to run it again to find out what —
// which is the one thing an operator just approved individually.
func TestBash_AFailingScriptReturnsItsOutputAndStatus(t *testing.T) {
	out, err := bashOut(t, t.TempDir(), "echo about to fail >&2; exit 3", 0)
	if err == nil {
		t.Fatal("a non-zero exit was reported as success")
	}
	if !strings.Contains(out, "about to fail") {
		t.Errorf("out = %q, want the output kept", out)
	}
	if !strings.Contains(out, "exit status 3") {
		t.Errorf("out = %q, want the status named", out)
	}
}

// A hung script is killed and reported, not left running.
func TestBash_ATimeoutIsReportedNotHung(t *testing.T) {
	start := time.Now()
	out, err := bashOut(t, t.TempDir(), "sleep 30", 300*time.Millisecond)
	if err == nil {
		t.Fatal("a timed-out script was reported as success")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s; the timeout did not fire", took)
	}
	if !strings.Contains(out, "killed after") {
		t.Errorf("out = %q, want the kill explained", out)
	}
}

// Large output is cut, and the cut is stated.
func TestBash_LargeOutputIsCappedAndSaysSo(t *testing.T) {
	out, err := bashOut(t, t.TempDir(),
		"i=0; while [ $i -lt 5000 ]; do printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\\n'; i=$((i+1)); done", 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out) > maxExecOutput+200 {
		t.Errorf("output was %d bytes, want it capped near %d", len(out), maxExecOutput)
	}
	if !strings.Contains(out, "there was more") {
		t.Errorf("the cut was silent: %q", out[max(0, len(out)-120):])
	}
}

// A script that printed nothing says so rather than returning "".
func TestBash_SilenceIsReportedAsSilence(t *testing.T) {
	out, err := bashOut(t, t.TempDir(), "true", 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out == "" {
		t.Error("an empty string cannot be told from a broken tool")
	}
}

func TestBash_AnEmptyScriptIsRefused(t *testing.T) {
	if _, err := bashOut(t, t.TempDir(), "", 0); err == nil {
		t.Error("an empty script was run")
	}
}

// The script survives quoting and newlines exactly as written.
//
// It is passed on stdin rather than as -c, so the bytes that run are the
// bytes the operator approved.
func TestBash_TheScriptRunsExactlyAsApproved(t *testing.T) {
	script := "printf '%s\\n' \"a 'quoted' string\"\nprintf 'second line\\n'"
	out, err := bashOut(t, t.TempDir(), script, 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "a 'quoted' string") || !strings.Contains(out, "second line") {
		t.Errorf("out = %q, want both lines exactly", out)
	}
}

// ScriptOf gives a UI the script to show, unescaped.
func TestScriptOf_ReturnsTheScriptForDisplay(t *testing.T) {
	args := json.RawMessage(`{"script":"go test ./...\nexit 0"}`)
	got := ScriptOf(args)
	if got != "go test ./...\nexit 0" {
		t.Errorf("ScriptOf = %q, want the script with its newline intact", got)
	}
}

func TestScriptOf_IsEmptyForAnythingElse(t *testing.T) {
	for _, args := range []string{`{"path":"x"}`, `not json`, `{}`} {
		if got := ScriptOf(json.RawMessage(args)); got != "" {
			t.Errorf("ScriptOf(%s) = %q, want empty", args, got)
		}
	}
}

// The tool declares itself as executing, which is what the gate judges.
func TestBash_DeclaresItselfAsExecuting(t *testing.T) {
	if e := Bash(t.TempDir(), 0).Effect; e != Executes {
		t.Errorf("effect = %q, want %q", e, Executes)
	}
}

// The timeout kills the script's CHILDREN too, not just the shell.
//
// THE BUG THIS EXISTS FOR, measured: `sleep 30` under a 300ms timeout
// took the full 30 seconds. CommandContext killed sh, but sleep survived
// holding the output pipe, and Run blocked until it finished. A timeout
// that waits for the runaway child is the hang it was added to prevent.
func TestBash_ATimeoutKillsTheScriptsChildrenToo(t *testing.T) {
	start := time.Now()
	// A background child that outlives its parent shell.
	_, err := bashOut(t, t.TempDir(), "sleep 30 & sleep 30", 300*time.Millisecond)
	if err == nil {
		t.Fatal("a timed-out script was reported as success")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s — the child kept the pipe open, so the timeout did not end the call", took)
	}
}

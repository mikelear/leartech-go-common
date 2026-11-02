package agentloop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// killedBySignal recognises a process killed by a signal (what an external
// `exec kill` produces), and returns which one.
func TestKilledBySignal_RecognisesATerminatedProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}()
	err := cmd.Wait()
	sig, ok := killedBySignal(err)
	if !ok {
		t.Fatalf("killedBySignal(%v) = _, false; want it recognised", err)
	}
	if sig != syscall.SIGTERM {
		t.Errorf("signal = %v, want SIGTERM", sig)
	}
}

// An ordinary non-zero exit is NOT a signal kill, so the model still sees its
// exit status rather than a "killed from outside" message.
func TestKilledBySignal_IgnoresAnOrdinaryExit(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 3").Run()
	if _, ok := killedBySignal(err); ok {
		t.Errorf("an exit-3 was misread as a signal kill")
	}
	if _, ok := killedBySignal(nil); ok {
		t.Errorf("a nil error was misread as a signal kill")
	}
}

// scriptSummary takes the first meaningful line, skipping blanks and comments.
func TestScriptSummary_TakesTheFirstMeaningfulLine(t *testing.T) {
	got := scriptSummary("\n# a comment\n\nls -la /tmp\nrm -rf x")
	if got != "ls -la /tmp" {
		t.Errorf("summary = %q, want the first real line", got)
	}
}

// An external kill of a bash run is reported as DELIBERATE, telling the model
// to stop rather than retry.
func TestBash_AnExternalKillIsReportedAsDeliberate(t *testing.T) {
	// A long bash run under a generous timeout, killed from "outside" by us
	// signalling its recorded pid through the registry — exactly the path
	// `ship-proven exec kill` takes.
	dir := t.TempDir()
	reg := NewExecRegistry(filepath.Join(dir, "reg"))
	tool := BashIn(t.TempDir(), 30*time.Second, ExecOpts{Registry: reg, ShellPID: os.Getpid()})

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		args, _ := json.Marshal(map[string]string{"script": "sleep 20"})
		out, err := tool.Run(args)
		done <- result{out, err}
	}()

	// Wait for the run to appear in the registry, then kill it.
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := reg.List()
		if len(list) == 1 {
			pid = list[0].PID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the run never appeared in the registry")
	}
	if err := reg.Kill(pid, false); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("a killed run reported success")
		}
		if !strings.Contains(r.out, "killed from outside") {
			t.Errorf("out = %q, want it to explain the external kill", r.out)
		}
		if !strings.Contains(r.out, "do not retry") && !strings.Contains(r.out, "Stop and wait") {
			t.Errorf("out = %q, want it to tell the model to stop", r.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the killed run did not return")
	}
}

// BashIn announces its run to the registry for the run's lifetime.
func TestBashIn_AnnouncesTheRunToTheRegistry(t *testing.T) {
	reg := NewExecRegistry(filepath.Join(t.TempDir(), "reg"))
	tool := BashIn(t.TempDir(), 30*time.Second, ExecOpts{Registry: reg, ShellPID: os.Getpid(), SessionID: func() string { return "sess-1" }})

	seen := make(chan Entry, 1)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if list, _ := reg.List(); len(list) == 1 {
				seen <- list[0]
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		close(seen)
	}()

	args, _ := json.Marshal(map[string]string{"script": "sleep 1"})
	if _, err := tool.Run(args); err != nil {
		t.Fatalf("run: %v", err)
	}
	e, ok := <-seen
	if !ok {
		t.Fatal("the run was never announced to the registry")
	}
	if e.Tool != "local__bash" || e.SessionID != "sess-1" {
		t.Errorf("entry = %+v, want local__bash / sess-1", e)
	}
	// After the run returns, the entry is gone.
	if list, _ := reg.List(); len(list) != 0 {
		t.Errorf("after the run, List = %v, want empty", list)
	}
}

// WaitIn announces its running attempt to the registry, so a long wait is
// visible to `exec list` and killable. The awaited command SLEEPS briefly so
// a single attempt is live long enough to observe, then the observer lets the
// wait finish by creating the readiness file.
func TestWaitIn_AnnouncesEachAttemptToTheRegistry(t *testing.T) {
	reg := NewExecRegistry(filepath.Join(t.TempDir(), "reg"))
	root := t.TempDir()
	tool := WaitIn(root, 10*time.Second, ExecOpts{Registry: reg, ShellPID: os.Getpid()})

	seen := make(chan Entry, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if list, _ := reg.List(); len(list) == 1 {
				seen <- list[0]
				_ = os.WriteFile(filepath.Join(root, "ready"), []byte("x"), 0o600)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.WriteFile(filepath.Join(root, "ready"), []byte("x"), 0o600)
		close(seen)
	}()

	// Each attempt sleeps 300ms before its test, so the attempt is live in
	// the registry long enough for the observer above to catch it, and the
	// first attempt fails (no file yet) so there IS a running attempt.
	ready := filepath.Join(root, "ready")
	// A non-leading sleep: stripLeadingSleep only removes a sleep at the FRONT,
	// so this one survives and keeps each attempt live ~300ms. The first
	// attempt fails (no file yet), so there is a running attempt to observe.
	args, _ := json.Marshal(map[string]string{"command": "test -f " + ready + " || { sleep 0.3; false; }"})
	if _, err := tool.Run(args); err != nil {
		t.Fatalf("wait: %v", err)
	}
	e, ok := <-seen
	if !ok {
		t.Fatal("no wait attempt was announced to the registry")
	}
	if e.Tool != "local__wait" {
		t.Errorf("entry.Tool = %q, want local__wait", e.Tool)
	}
}

// runInGroup kills the whole group on context cancel, so a wait attempt's
// children do not outlive it (the same property bash relies on).
func TestRunInGroup_CancelKillsTheGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	_, _ = runInGroup(ctx, t.TempDir(), "sleep 30 & sleep 30", "local__bash", "sleep", ExecOpts{})
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s — a child kept the pipe open past cancel", took)
	}
}

// runInGroup records the session id resolved AT RUN TIME, not at build time —
// a shell that registers its session after building its tools still shows the
// right id in `exec list`.
func TestRunInGroup_RecordsTheResolvedSessionID(t *testing.T) {
	reg := NewExecRegistry(filepath.Join(t.TempDir(), "reg"))
	id := "" // empty at build time
	tool := BashIn(t.TempDir(), 30*time.Second, ExecOpts{
		Registry: reg, ShellPID: os.Getpid(),
		SessionID: func() string { return id },
	})
	id = "registered-late" // set before the run, as a late registration would

	seen := make(chan Entry, 1)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if list, _ := reg.List(); len(list) == 1 {
				seen <- list[0]
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		close(seen)
	}()
	args, _ := json.Marshal(map[string]string{"script": "sleep 1"})
	if _, err := tool.Run(args); err != nil {
		t.Fatalf("run: %v", err)
	}
	e, ok := <-seen
	if !ok {
		t.Fatal("run not announced")
	}
	if e.SessionID != "registered-late" {
		t.Errorf("SessionID = %q, want the late-resolved id", e.SessionID)
	}
}

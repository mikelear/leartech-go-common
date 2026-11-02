package agentloop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A nil registry (built from "") is a no-op on every method, so a caller
// without a registry dir passes nil and the tools behave as before.
func TestExecRegistry_NilIsANoOp(t *testing.T) {
	var r *ExecRegistry = NewExecRegistry("")
	if r != nil {
		t.Fatalf("NewExecRegistry(\"\") = %v, want nil", r)
	}
	// record returns a usable no-op cleanup; List is empty; Kill refuses.
	cleanup := r.record(Entry{PID: 1})
	cleanup()
	if list, err := r.List(); err != nil || len(list) != 0 {
		t.Fatalf("nil List = %v, %v; want empty, nil", list, err)
	}
	if err := r.Kill(1, false); err == nil {
		t.Error("nil Kill reported success; want a refusal")
	}
}

// record writes an entry while the run is live and removes it after.
func TestExecRegistry_RecordsWhileRunningAndRemovesAfter(t *testing.T) {
	dir := t.TempDir()
	r := NewExecRegistry(dir)
	cleanup := r.record(Entry{PID: os.Getpid(), ShellPID: os.Getpid(), Tool: "local__bash", What: "echo hi", Started: time.Now()})
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].PID != os.Getpid() {
		t.Fatalf("while running, List = %v, want one entry for this pid", list)
	}
	cleanup()
	list, err = r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("after cleanup, List = %v, want empty", list)
	}
}

// An unwritable dir does not break the run: record returns a cleanup and the
// script is simply unlisted.
func TestExecRegistry_AnUnwritableDirDoesNotBreakTheRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "file-not-dir")
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewExecRegistry(dir) // dir is a FILE, so MkdirAll under it fails
	cleanup := r.record(Entry{PID: 1, ShellPID: os.Getpid()})
	if cleanup == nil {
		t.Fatal("record returned a nil cleanup")
	}
	cleanup() // must not panic
}

// List returns the running entries, newest first.
func TestExecRegistry_ListReturnsRunningEntries(t *testing.T) {
	dir := t.TempDir()
	r := NewExecRegistry(dir)
	r.record(Entry{PID: 101, ShellPID: os.Getpid(), Started: time.Now().Add(-2 * time.Second)})
	r.record(Entry{PID: 102, ShellPID: os.Getpid(), Started: time.Now()})
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List = %d entries, want 2", len(list))
	}
	if list[0].PID != 102 {
		t.Errorf("newest first: got pid %d first, want 102", list[0].PID)
	}
}

// List reaps an entry whose owning shell PID is gone — a shell that was
// SIGKILLed never ran its cleanup, and the listing must not show its run.
func TestExecRegistry_ListReapsEntriesWhoseShellIsGone(t *testing.T) {
	dir := t.TempDir()
	r := NewExecRegistry(dir)
	deadShell := impossiblePID(t)
	r.record(Entry{PID: 201, ShellPID: deadShell, Started: time.Now()})
	r.record(Entry{PID: 202, ShellPID: os.Getpid(), Started: time.Now()})
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].PID != 202 {
		t.Fatalf("List = %v, want only the live-shell entry (202)", list)
	}
	// The stale file is actually removed, not just filtered.
	if _, err := os.Stat(filepath.Join(dir, "201.json")); !os.IsNotExist(err) {
		t.Error("the stale entry's file was not reaped")
	}
}

// List of a dir that does not exist is empty, not an error — a fresh machine
// has run nothing yet.
func TestExecRegistry_ListOfAnAbsentDirIsEmpty(t *testing.T) {
	r := NewExecRegistry(filepath.Join(t.TempDir(), "never-created"))
	list, err := r.List()
	if err != nil {
		t.Fatalf("List of absent dir: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List = %v, want empty", list)
	}
}

// Kill signals a run the registry knows. We prove the signal lands by killing
// a real child we started and recorded.
func TestExecRegistry_KillSignalsAKnownRun(t *testing.T) {
	dir := t.TempDir()
	r := NewExecRegistry(dir)

	// A real sleeper in its own group, recorded as if a tool had started it.
	pid := startSleeper(t)
	r.record(Entry{PID: pid, ShellPID: os.Getpid(), Tool: "local__bash", What: "sleep", Started: time.Now()})

	if err := r.Kill(pid, false); err != nil {
		t.Fatalf("Kill of a known run: %v", err)
	}
	if !waitGone(pid) {
		t.Fatalf("pid %d still alive after Kill", pid)
	}
}

// Kill refuses a PID the registry does not list — the command manages the
// shells' own scripts, not arbitrary processes.
func TestExecRegistry_KillRefusesAPIDItDoesNotKnow(t *testing.T) {
	r := NewExecRegistry(t.TempDir())
	err := r.Kill(424242, false)
	if err == nil {
		t.Fatal("Kill of an unknown pid reported success")
	}
	if !strings.Contains(err.Error(), "424242") {
		t.Errorf("error = %q, want it to name the pid", err)
	}
}

// A corrupt entry file is dropped by List, not surfaced as a run.
func TestExecRegistry_ListDropsACorruptEntry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "7.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewExecRegistry(dir)
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("List = %v, want the corrupt file dropped", list)
	}
}

// --- helpers ---

// impossiblePID returns a pid that names no live process, for reaping tests.
func impossiblePID(t *testing.T) int {
	t.Helper()
	// A very high pid is almost never live; verify with signal 0.
	for _, cand := range []int{4000000, 3999999, 3999998} {
		if !processAlive(cand) {
			return cand
		}
	}
	t.Skip("could not find a dead pid to use")
	return 0
}

// startSleeper launches `sleep 60` in its own process group (as the tools do,
// with Setpgid) and returns its pid, cleaning it up at test end.
func startSleeper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	pid := cmd.Process.Pid
	// Reap in the background: once killed, Wait() clears the zombie so
	// processAlive (signal 0) reports it gone. Without this, a SIGTERM'd
	// child lingers as a zombie and the kill looks to have failed.
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-reaped
	})
	return pid
}

// waitGone polls until a pid is no longer alive, up to a few seconds.
func waitGone(pid int) bool {
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

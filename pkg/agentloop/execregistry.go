package agentloop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// ExecRegistry records the local__bash and local__wait invocations that are
// RUNNING RIGHT NOW, across every ship-proven shell on this machine, so a
// person at a SECOND terminal can list them and kill one.
//
// WHY A DIRECTORY OF FILES AND NOT A SOCKET. The shells do not talk to each
// other and should not have to: each is its own process, started whenever,
// by whoever. A file per live run in a shared directory is the simplest
// thing that lets a cold `ship-proven exec list` — a process that shares no
// memory with any shell — see them all. It is the same shape the resume and
// debug caches already use under ~/.config/leartech.
//
// ONE FILE PER RUNNING SCRIPT, named by the child's PID, deleted when the
// script ends. A shell that runs ten scripts in its life writes and removes
// ten files, at most (its concurrency) at once — and today that is
// one, because a shell runs one tool at a time. The PID in the name is what
// `exec kill` targets.
//
// LIVENESS IS EXACT, because the owner PID is a real process on this
// machine: List reaps any entry whose owning shell is gone (a crash that
// skipped the deferred cleanup), so the listing does not show a run that
// cannot exist. // proven-by: TestExecRegistry_ListReapsEntriesWhoseShellIsGone
type ExecRegistry struct {
	dir string
}

// Entry is one running exec, as written to disk and read back by `exec list`.
type Entry struct {
	// PID is the child shell's process id — the handle `exec kill` uses, and
	// the file's name. It is the process group leader (the tool sets Setpgid),
	// so killing it reaches the whole script tree.
	PID int `json:"pid"`
	// ShellPID is the ship-proven shell process that started this run. When it
	// is gone the entry is stale and List reaps it.
	ShellPID int `json:"shell_pid"`
	// SessionID is the owning shell's session id, for display. May be empty:
	// a shell that has not registered a session yet still runs scripts.
	SessionID string `json:"session_id,omitempty"`
	// Tool is "local__bash" or "local__wait" — which of the two is running.
	Tool string `json:"tool"`
	// What is a short, single-line description of the script/command, so the
	// listing tells a person WHAT they would be killing, not just a number.
	What string `json:"what"`
	// Started is when the run began, for an "age" column.
	Started time.Time `json:"started"`
}

// NewExecRegistry returns a registry rooted at dir. A dir of "" disables it:
// NewExecRegistry("") returns nil, and every method on a nil *ExecRegistry is
// a no-op, so a caller that has no registry dir (an agent in a Job, a test
// that does not care) passes nil and the tools behave exactly as before.
//
// proven-by: TestExecRegistry_NilIsANoOp
func NewExecRegistry(dir string) *ExecRegistry {
	if dir == "" {
		return nil
	}
	return &ExecRegistry{dir: dir}
}

// Record announces a running exec and returns a cleanup that removes it. It is
// the exported seam the SHELL uses nowhere (the tools call the internal path)
// and that TESTS in other packages use to seed the registry with a known
// entry. In-process callers get the same write-then-remove contract as the
// tools' own announcements.
//
// proven-by: TestExecRegistry_RecordsWhileRunningAndRemovesAfter
func (r *ExecRegistry) Record(e Entry) func() {
	return r.record(e)
}

// record writes the entry for a running script and returns a function that
// removes it. The caller defers the returned func, so the entry exists for
// exactly the life of the run — written before Run, gone after it, whether
// the run returned, failed or was killed.
//
// A WRITE FAILURE DOES NOT STOP THE RUN. The registry is a convenience for a
// second terminal, not part of what makes a script correct: if the directory
// is unwritable the script still runs, just unlisted. Returning an error here
// and failing the tool would let a broken cache dir break the shell, which is
// the opposite of the trade this feature is worth.
//
// proven-by: TestExecRegistry_RecordsWhileRunningAndRemovesAfter
// proven-by: TestExecRegistry_AnUnwritableDirDoesNotBreakTheRun
func (r *ExecRegistry) record(e Entry) func() {
	if r == nil {
		return func() {}
	}
	if err := os.MkdirAll(r.dir, 0o750); err != nil {
		return func() {}
	}
	path := r.pathFor(e.PID)
	b, err := json.Marshal(e)
	if err != nil {
		return func() {}
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

func (r *ExecRegistry) pathFor(pid int) string {
	return filepath.Join(r.dir, strconv.Itoa(pid)+".json")
}

// List returns the running execs, newest first, reaping any whose owning
// shell PID has gone away.
//
// REAPING IS HOW A STALE ENTRY DIES. A shell that is SIGKILLed does not run its
// deferred cleanup, so its entry lingers. List checks each entry's ShellPID
// with signal 0 (which tests existence without touching the process) and
// deletes the file when the shell is gone, so a cold reader does not report a
// run whose owning shell has died. The child PID itself is not probed: the file's
// existence IS the claim, and the owning shell's death is what invalidates it.
//
// proven-by: TestExecRegistry_ListReturnsRunningEntries
// proven-by: TestExecRegistry_ListReapsEntriesWhoseShellIsGone
// proven-by: TestExecRegistry_ListOfAnAbsentDirIsEmpty
func (r *ExecRegistry) List() ([]Entry, error) {
	if r == nil {
		return nil, nil
	}
	des, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		if de.IsDir() || filepath.Ext(de.Name()) != ".json" {
			continue
		}
		path := filepath.Join(r.dir, de.Name())
		b, err := os.ReadFile(path) // #nosec G304 -- listed from our own dir
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			// A half-written or corrupt entry is not a running run; drop it.
			_ = os.Remove(path)
			continue
		}
		if !processAlive(e.ShellPID) {
			_ = os.Remove(path)
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

// Kill terminates a running exec BY PID, but only one this registry knows —
// i.e. one started by a ship-proven shell. A PID that is not in the registry
// is REFUSED, because this command exists to manage the shells' own scripts,
// not to be a general-purpose kill: killing an arbitrary process is what the
// operator's own shell is for, and a tool that would do it from a session id
// is a foot-gun with our name on it.
//
// It signals the whole PROCESS GROUP (negative pid), the same group the tool
// put the script in with Setpgid, so children die with the shell and nothing
// is orphaned. SIGTERM by default so a script can finish a write and exit;
// force sends SIGKILL for one that ignores TERM.
//
// 404 SHAPE: a PID the registry does not list returns an error naming it, so
// `exec kill 999` on a number that is not ours says so rather than reaching
// out and killing it. // proven-by: TestExecRegistry_KillRefusesAPIDItDoesNotKnow
//
// proven-by: TestExecRegistry_KillSignalsAKnownRun
// proven-by: TestExecRegistry_KillRefusesAPIDItDoesNotKnow
func (r *ExecRegistry) Kill(pid int, force bool) error {
	if r == nil {
		return fmt.Errorf("no exec registry on this machine")
	}
	entries, err := r.List()
	if err != nil {
		return err
	}
	known := false
	for _, e := range entries {
		if e.PID == pid {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("no ship-proven script is running with pid %d "+
			"(exec kill only manages scripts started by a ship-proven shell; "+
			"run `ship-proven exec list` to see them)", pid)
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	// Negative pid: the whole process group the tool created with Setpgid.
	if err := syscall.Kill(-pid, sig); err != nil {
		return fmt.Errorf("signalling pid %d: %w", pid, err)
	}
	return nil
}

// processAlive reports whether a pid names a live process, using signal 0 —
// which performs the permission and existence checks of kill WITHOUT sending
// a signal. ESRCH means no such process; EPERM means it exists but we may not
// signal it (still alive). Any other error is treated as "gone", because the
// caller's only use is reaping, and a probe that is not interpretable should not
// keep a stale entry forever.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}

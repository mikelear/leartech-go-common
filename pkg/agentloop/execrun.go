package agentloop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ExecOpts carries what an exec needs to announce itself in the ExecRegistry,
// so a second terminal can see and kill it. It is OPTIONAL end to end: the
// zero value (Registry nil) means "do not announce", and Bash/Wait built
// without it behave exactly as they did before this file existed.
//
// WHY A STRUCT AND NOT MORE ARGUMENTS. Bash and Wait already take (root,
// timeout); adding three more positional parameters to each — and to every
// test that builds one — would be noise at every call site that does not
// care. One optional opts value keeps the common call (a test, an agent in a
// Job) unchanged and lets the shell pass the extra context in one place.
type ExecOpts struct {
	// Registry is where a running script announces itself. Nil disables it.
	Registry *ExecRegistry
	// ShellPID is the ship-proven shell process announcing the run, used by
	// the registry to reap entries whose shell has died. Defaults to this
	// process's PID when zero.
	ShellPID int
	// SessionID resolves the owning shell's session id, shown in `exec list`.
	// It is a FUNCTION, not a string, because a shell registers its session
	// AFTER it builds its tools — a static string captured at build time would
	// be empty for the rest of the run. Nil, or a func returning "", simply
	// lists the run without a session id. // proven-by: TestRunInGroup_RecordsTheResolvedSessionID
	SessionID func() string
}

// runInGroup runs one shell command (script or wait-command) in its own
// process group, announces it to the registry for its lifetime, and returns
// the combined output plus the raw run error and the context.
//
// IT IS THE ONE PLACE A CHILD PROCESS IS SPAWNED for both tools, so the
// Setpgid-and-kill-the-group behaviour that stops a timeout hanging on a
// surviving child — and that lets `exec kill` reach the whole tree — lives
// once rather than being copied into exec.go and wait.go. The callers differ
// only in how they INTERPRET the result (bash reports a timeout, wait polls),
// so interpretation stays with them.
//
// proven-by: TestBash_ATimeoutKillsTheScriptsChildrenToo
// proven-by: TestExecRegistry_RecordsWhileRunningAndRemovesAfter
func runInGroup(ctx context.Context, dir, input, tool, what string, opts ExecOpts) (string, error) {
	cmd := exec.CommandContext(ctx, "sh") // #nosec G204 -- an approved script/command, gated by Registry.Run
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader([]byte(input))
	cmd.Env = os.Environ()

	// Setpgid puts the script in its own group so the whole tree can be
	// killed — by the timeout below, and by `exec kill` from another
	// terminal. WaitDelay forces the pipes shut for anything that survives.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	// Start, not Run, so the child's PID is known BEFORE it finishes — that
	// PID is what the registry records and what `exec kill` targets.
	if err := cmd.Start(); err != nil {
		return "", err
	}

	shellPID := opts.ShellPID
	if shellPID == 0 {
		shellPID = os.Getpid()
	}
	sessionID := ""
	if opts.SessionID != nil {
		sessionID = opts.SessionID()
	}
	cleanup := opts.Registry.record(Entry{
		PID:       cmd.Process.Pid,
		ShellPID:  shellPID,
		SessionID: sessionID,
		Tool:      tool,
		What:      what,
		Started:   time.Now(),
	})
	defer cleanup()

	runErr := cmd.Wait()
	return out.String(), runErr
}

// killedBySignal reports whether a run error is a process killed by a signal,
// and which signal. It is how an EXTERNAL kill (`ship-proven exec kill`) is
// told apart from an ordinary non-zero exit: a signalled process has
// ExitCode -1 and a WaitStatus that says Signaled. The timeout path is NOT
// routed here — the caller checks context.DeadlineExceeded first, because a
// timeout also kills by signal but means something different to the model.
//
// proven-by: TestKilledBySignal_RecognisesATerminatedProcess
// proven-by: TestKilledBySignal_IgnoresAnOrdinaryExit
func killedBySignal(runErr error) (syscall.Signal, bool) {
	if runErr == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if !errors.As(runErr, &ee) {
		return 0, false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, false
	}
	return ws.Signal(), true
}

// scriptSummary renders a one-line description of a script/command for the
// `exec list` listing, so a person sees WHAT a run is before killing it —
// the first non-empty, non-comment line, truncated.
//
// proven-by: TestScriptSummary_TakesTheFirstMeaningfulLine
func scriptSummary(script string) string {
	const max = 80
	for _, line := range strings.Split(script, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' {
			continue
		}
		if len(t) > max {
			return t[:max-1] + "…"
		}
		return t
	}
	t := strings.TrimSpace(script)
	if len(t) > max {
		return t[:max-1] + "…"
	}
	return t
}

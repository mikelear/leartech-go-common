package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitOut(t *testing.T, root, command string, timeout time.Duration) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return Wait(root, timeout).Run(args)
}

// It returns as soon as the command succeeds, with no waiting.
func TestWait_ReturnsWhenTheCommandSucceeds(t *testing.T) {
	start := time.Now()
	out, err := waitOut(t, t.TempDir(), "true", time.Minute)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %s; a command that succeeds first time should not wait", took)
	}
	if !strings.Contains(out, "condition met") {
		t.Errorf("out = %q, want it to say the condition was met", out)
	}
}

// It polls, failing then succeeding, and returns once the command passes.
func TestWait_PollsUntilSuccess(t *testing.T) {
	root := t.TempDir()
	// Succeeds only once a marker file exists; a background sleep creates it
	// shortly after the wait starts, so the first attempt fails and a later
	// one passes.
	script := "test -f " + root + "/ready"
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = waitForCommand(root, "touch "+root+"/ready", time.Minute, time.Second, ExecOpts{})
	}()
	out, err := waitForCommand(root, script, 10*time.Second, 200*time.Millisecond, ExecOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "condition met") {
		t.Errorf("out = %q, want success after polling", out)
	}
}

// When the deadline passes it gives up, says so, and does NOT look like a pass.
func TestWait_GivesUpAtTheDeadlineAndSaysSo(t *testing.T) {
	start := time.Now()
	out, err := waitForCommand(t.TempDir(), "false", 600*time.Millisecond, 200*time.Millisecond, ExecOpts{})
	if err == nil {
		t.Fatal("a wait that never succeeded was reported as success")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s; the deadline did not bound the wait", took)
	}
	if !strings.Contains(out, "did not hold") || !strings.Contains(out, "NOT a") {
		t.Errorf("out = %q, want it to say the condition did not hold and is not a pass", out)
	}
}

// The deadline is named in the give-up text, so the model reasons on the
// real window rather than a guess.
func TestWait_ReportsTheDeadlineInTheResult(t *testing.T) {
	out, _ := waitForCommand(t.TempDir(), "false", 500*time.Millisecond, 200*time.Millisecond, ExecOpts{})
	if !strings.Contains(out, "500ms") {
		t.Errorf("out = %q, want the deadline named", out)
	}
}

func TestWait_AnEmptyCommandIsRefused(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "   "})
	if _, err := Wait(t.TempDir(), time.Minute).Run(args); err == nil {
		t.Error("an empty command was run")
	}
}

func TestWait_DeclaresItselfAsExecuting(t *testing.T) {
	if e := Wait(t.TempDir(), 0).Effect; e != Executes {
		t.Errorf("effect = %q, want %q", e, Executes)
	}
}

// A zero timeout falls back to the default, never to "no deadline".
func TestWait_AZeroTimeoutUsesTheDefault(t *testing.T) {
	desc := Wait(t.TempDir(), 0).Description
	if !strings.Contains(desc, DefaultWaitTimeout.String()) {
		t.Errorf("description = %q, want it to name the default timeout", desc)
	}
}

func TestLeadingSleep_MatchesALeadingSleep(t *testing.T) {
	for _, s := range []string{
		"sleep 100; gh pr checks 42",
		"  sleep 5\necho done",
		"# waiting for the pipeline\nsleep 120; kubectl get po",
	} {
		if !leadingSleep(s) {
			t.Errorf("leadingSleep(%q) = false, want true", s)
		}
	}
}

func TestLeadingSleep_IgnoresASleepThatIsNotFirst(t *testing.T) {
	for _, s := range []string{
		"echo start; sleep 2; echo done",
		"for i in 1 2 3; do sleep 1; done",
		"kubectl rollout status deploy/x",
	} {
		if leadingSleep(s) {
			t.Errorf("leadingSleep(%q) = true, want false", s)
		}
	}
}

func TestLeadingSleep_IgnoresAScriptWithNoSleep(t *testing.T) {
	if leadingSleep("ls -la") {
		t.Error("a script with no sleep matched")
	}
}

func TestStripLeadingSleep_RemovesALeadingSleepAndExplains(t *testing.T) {
	for _, cmd := range []string{
		"sleep 100; gh pr checks 42",
		"sleep 30 && kubectl get po",
		"  sleep 45 ;  kubectl -n x get pods",
	} {
		got, note := stripLeadingSleep(cmd)
		if strings.HasPrefix(strings.TrimSpace(got), "sleep") {
			t.Errorf("stripLeadingSleep(%q) left the sleep: %q", cmd, got)
		}
		if note == "" {
			t.Errorf("stripLeadingSleep(%q) stripped but did not explain", cmd)
		}
	}
}

func TestStripLeadingSleep_LeavesACommandWithoutALeadingSleep(t *testing.T) {
	for _, cmd := range []string{
		"gh pr checks 42",
		"until kubectl get po | grep Running; do sleep 5; done",
	} {
		got, note := stripLeadingSleep(cmd)
		if got != cmd || note != "" {
			t.Errorf("stripLeadingSleep(%q) = (%q,%q), want it unchanged", cmd, got, note)
		}
	}
}

// A sleep INSIDE the command (a retry backoff within a loop) is left alone —
// only a leading sleep-then-real-command is the anti-pattern.
func TestStripLeadingSleep_LeavesASleepInsideTheCommand(t *testing.T) {
	cmd := "for i in 1 2 3; do sleep 2; kubectl get po; done"
	got, note := stripLeadingSleep(cmd)
	if got != cmd || note != "" {
		t.Errorf("stripLeadingSleep(%q) = (%q,%q), want it unchanged", cmd, got, note)
	}
}

// A bare `sleep 5` with nothing after it is left as-is: there is nothing to
// wait ON, and stripping to empty would be a different command.
func TestStripLeadingSleep_LeavesABareSleep(t *testing.T) {
	got, note := stripLeadingSleep("sleep 5")
	if got != "sleep 5" || note != "" {
		t.Errorf("stripLeadingSleep bare sleep = (%q,%q), want unchanged", got, note)
	}
}

// End to end: a sleep-prefixed wait command still succeeds, AND the result
// carries the note — so the model learns the prefix was pointless.
func TestWait_StripsALeadingSleepFromTheCommand(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "sleep 2; true"})
	start := time.Now()
	out, err := Wait(t.TempDir(), time.Minute).Run(args)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %s; the leading sleep was not stripped (the first poll waited 2s)", took)
	}
	if !strings.Contains(out, "dropped a leading") {
		t.Errorf("out = %q, want the note that the sleep was dropped", out)
	}
}

// A wait is ONE killable child for its whole life, not one child per attempt
// with nothing listed in the gaps. We start a wait that never succeeds, watch
// the registry show exactly one running entry continuously, then kill it and
// confirm the wait ends and reports a deliberate external kill.
func TestWait_IsOneKillableChildForTheWholeWait(t *testing.T) {
	reg := NewExecRegistry(filepath.Join(t.TempDir(), "reg"))
	root := t.TempDir()

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		// Never succeeds; a generous deadline so the kill, not the deadline,
		// is what ends it. Short interval so the loop is clearly cycling.
		out, err := waitForCommand(root, "false", 20*time.Second, 300*time.Millisecond,
			ExecOpts{Registry: reg, ShellPID: os.Getpid()})
		done <- result{out, err}
	}()

	// Across several poll intervals there is always exactly one entry — the
	// single loop child — never zero (the old gap) and never many.
	var pid int
	sawContinuous := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := reg.List()
		if len(list) == 1 {
			pid = list[0].PID
			sawContinuous++
		} else if len(list) > 1 {
			t.Fatalf("saw %d entries; a wait must be ONE child", len(list))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pid == 0 || sawContinuous < 10 {
		t.Fatalf("the wait was not continuously listed as one child (saw=%d)", sawContinuous)
	}

	if err := reg.Kill(pid, false); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("a killed wait reported success")
		}
		if !strings.Contains(r.out, "killed from outside") {
			t.Errorf("out = %q, want the external kill explained", r.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the killed wait did not return")
	}
	// The entry is gone once the wait returns.
	if list, _ := reg.List(); len(list) != 0 {
		t.Errorf("after the wait ended, List = %v, want empty", list)
	}
}

// An external kill of a wait is reported as deliberate — the same contract as
// local__bash — so the model stops rather than treating it as a failed check.
func TestWait_AnExternalKillIsReportedAsDeliberate(t *testing.T) {
	reg := NewExecRegistry(filepath.Join(t.TempDir(), "reg"))
	root := t.TempDir()
	done := make(chan error, 1)
	var out string
	go func() {
		o, err := waitForCommand(root, "false", 20*time.Second, 300*time.Millisecond,
			ExecOpts{Registry: reg, ShellPID: os.Getpid()})
		out = o
		done <- err
	}()
	var pid int
	for i := 0; i < 50 && pid == 0; i++ {
		if list, _ := reg.List(); len(list) == 1 {
			pid = list[0].PID
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("wait never registered")
	}
	if err := reg.Kill(pid, false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("killed wait reported success")
	}
	if !strings.Contains(out, "do not retry") && !strings.Contains(out, "Stop and wait") {
		t.Errorf("out = %q, want it to tell the model to stop", out)
	}
}

// The loop script succeeds immediately for a command that already passes.
func TestWaitLoopScript_SucceedsImmediatelyOnAPassingCommand(t *testing.T) {
	script := waitLoopScript("true", 10*time.Second, time.Second)
	start := time.Now()
	_, err := runInGroup(contextTODO(), t.TempDir(), script, "local__wait", "true", ExecOpts{})
	if err != nil {
		t.Fatalf("a passing command should exit 0: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("took %s; a passing command should return at once", took)
	}
}

// The loop script exits with the deadline sentinel (97) when it never passes.
func TestWaitLoopScript_ExitsWithTheDeadlineSentinelOnTimeout(t *testing.T) {
	script := waitLoopScript("false", 1*time.Second, 300*time.Millisecond)
	_, err := runInGroup(contextTODO(), t.TempDir(), script, "local__wait", "false", ExecOpts{})
	ee := exitErr(t, err)
	if ee != 97 {
		t.Errorf("exit code = %d, want the deadline sentinel 97", ee)
	}
}

// shSingleQuote wraps a command for sh -c, escaping embedded quotes, so a
// command with its own quoting reaches sh intact.
func TestShSingleQuote_WrapsAndEscapes(t *testing.T) {
	got := shSingleQuote(`echo "a 'b' c"`)
	want := `'echo "a '\''b'\'' c"'`
	if got != want {
		t.Errorf("shSingleQuote = %q, want %q", got, want)
	}
}

func contextTODO() context.Context { return context.Background() }

func exitErr(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("error %v is not an *exec.ExitError", err)
	}
	return ee.ExitCode()
}

// THE REGRESSION THIS BRANCH FIXES. A wait that reaches its OWN deadline is
// ended by the outer context's SIGKILL, which is a signalled death. Before the
// fix that was read by killedBySignal as a person running `ship-proven exec
// kill`, so a plain timeout reported itself as a deliberate external kill and
// told the model to stop and wait for a human who was never there. The loop
// interval here is far longer than the deadline, so the script is mid-`sleep`
// when the context fires and cannot reach its own exit-97 branch — the exact
// shape that triggered the false "user killed it" in a live session.
//
// proven-by is this test; it must report a give-up, never an external kill.
func TestWait_AnOwnDeadlineIsNotReportedAsAnExternalKill(t *testing.T) {
	reg := NewExecRegistry(filepath.Join(t.TempDir(), "reg"))
	out, err := waitForCommand(t.TempDir(), "false", 1*time.Second, 30*time.Second,
		ExecOpts{Registry: reg, ShellPID: os.Getpid()})
	if err == nil {
		t.Fatal("a wait that never passes must return an error")
	}
	if strings.Contains(out, "killed from outside") {
		t.Fatalf("own deadline reported as an external kill:\n%s", out)
	}
	if !strings.Contains(err.Error(), "gave up after") {
		t.Errorf("err = %v, want a give-up", err)
	}
	if !strings.Contains(out, "did not hold within") {
		t.Errorf("out = %q, want the deadline said out loud", out)
	}
}

// clampWaitTimeout: zero means no override, so the default is used untouched.
func TestClampWaitTimeout_ZeroUsesTheDefault(t *testing.T) {
	eff, note := clampWaitTimeout(0, 7*time.Minute)
	if eff != 7*time.Minute || note != "" {
		t.Fatalf("got (%s,%q), want (7m0s,\"\")", eff, note)
	}
}

// clampWaitTimeout: a positive value is taken as seconds and used as-is.
func TestClampWaitTimeout_UsesAPositiveOverride(t *testing.T) {
	eff, note := clampWaitTimeout(90, 15*time.Minute)
	if eff != 90*time.Second || note != "" {
		t.Fatalf("got (%s,%q), want (1m30s,\"\")", eff, note)
	}
}

// clampWaitTimeout: anything above the ceiling is clamped down and said so.
func TestClampWaitTimeout_ClampsAboveTheCeilingAndSaysSo(t *testing.T) {
	eff, note := clampWaitTimeout(int(MaxWaitTimeout.Seconds())+600, 15*time.Minute)
	if eff != MaxWaitTimeout {
		t.Errorf("effective = %s, want the ceiling %s", eff, MaxWaitTimeout)
	}
	if !strings.Contains(note, "clamped to") {
		t.Errorf("note = %q, want it to explain the clamp", note)
	}
}

// local__wait honours a per-call timeout_seconds override: a 1-second override
// on a never-passing command gives up in about a second, not the default.
func TestWait_HonoursATimeoutOverride(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "false", "timeout_seconds": 1})
	start := time.Now()
	out, err := Wait(t.TempDir(), DefaultWaitTimeout).Run(args)
	took := time.Since(start)
	if err == nil {
		t.Fatal("a never-passing command must give up")
	}
	if took > 6*time.Second {
		t.Errorf("took %s; a 1s override should give up quickly, not wait the default", took)
	}
	if strings.Contains(out, "killed from outside") {
		t.Errorf("an override give-up must not read as an external kill:\n%s", out)
	}
}

// local__wait clamps an over-the-ceiling override and tells the model it did,
// so the model does not reason on a deadline that was never used.
func TestWait_ClampsAnOverTheCeilingTimeout(t *testing.T) {
	// A passing command returns at once; we only check the clamp note rides along.
	args, _ := json.Marshal(map[string]any{"command": "true", "timeout_seconds": int(MaxWaitTimeout.Seconds()) + 600})
	out, err := Wait(t.TempDir(), DefaultWaitTimeout).Run(args)
	if err != nil {
		t.Fatalf("a passing command should succeed: %v", err)
	}
	if !strings.Contains(out, "clamped to") {
		t.Errorf("out = %q, want the clamp explained", out)
	}
}

// The wait description names the override and its cap, so a model that wants a
// shorter or longer deadline knows the argument exists.
func TestWait_TheDescriptionNamesTheOverride(t *testing.T) {
	desc := Wait(t.TempDir(), DefaultWaitTimeout).Description
	if !strings.Contains(desc, "timeout_seconds") {
		t.Errorf("description = %q, want it to name the override", desc)
	}
	if !strings.Contains(desc, MaxWaitTimeout.String()) {
		t.Errorf("description = %q, want it to name the cap", desc)
	}
}

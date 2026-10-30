package agentloop

import (
	"encoding/json"
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
		_, _ = waitForCommand(root, "touch "+root+"/ready", time.Minute, time.Second)
	}()
	out, err := waitForCommand(root, script, 10*time.Second, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "condition met") {
		t.Errorf("out = %q, want success after polling", out)
	}
	if !strings.Contains(out, "attempt") {
		t.Errorf("out = %q, want the attempt count reported", out)
	}
}

// When the deadline passes it gives up, says so, and does NOT look like a pass.
func TestWait_GivesUpAtTheDeadlineAndSaysSo(t *testing.T) {
	start := time.Now()
	out, err := waitForCommand(t.TempDir(), "false", 600*time.Millisecond, 200*time.Millisecond)
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
	out, _ := waitForCommand(t.TempDir(), "false", 500*time.Millisecond, 200*time.Millisecond)
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

package agentloop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// DefaultWaitTimeout bounds one wait.
//
// LONG WHERE DefaultExecTimeout IS SHORT, and that difference is the whole
// point of the tool. local__bash kills at two minutes because a hung
// command there is a stuck run with nobody watching; local__wait exists
// precisely to sit idle for a real event — a pipeline turning green, a pod
// going Ready — which routinely takes longer than two minutes. Capping it
// at bash's ceiling would make the tool it replaces no better than the
// sleep it replaces.
//
// Fifteen minutes is a ceiling, not a target: the tool returns the instant
// its command succeeds, so a check that passes in forty seconds costs forty
// seconds. The ceiling is the backstop for an event that does not arrive.
const DefaultWaitTimeout = 15 * time.Minute

// defaultWaitInterval is how often the command is retried while waiting.
//
// The command runs, and if it fails the tool sleeps this long and runs it
// again, until it succeeds or the deadline passes. Five seconds is short
// enough that success is noticed promptly and long enough that polling a
// remote (gh, kubectl) is not itself the load.
const defaultWaitInterval = 5 * time.Second

// Wait blocks until a command succeeds, or a deadline passes.
//
// WHY IT EXISTS, AND WHY IT IS NOT local__bash. A model watching for an
// event with nothing to subscribe to reaches for `sleep N` inside local__bash:
// it guesses how long the event will take, sleeps blind, then checks. Two
// failure modes follow, both measured on real runs. If the guess is short,
// the check runs too early, fails, and the model sleeps again — a poll loop
// where every iteration re-sends the whole conversation and is paid for by
// the token. If the guess is long, local__bash's two-minute timeout kills
// the sleep WITHOUT TELLING THE MODEL it had a deadline at all, so a
// `sleep 300` silently becomes `sleep 120` and the model reasons on a
// falsehood.
//
// local__wait removes both. It takes the COMMAND that defines success, not
// a duration, and returns the moment that command exits 0 — so there is no
// duration to guess. Its deadline is long (see DefaultWaitTimeout) and is
// STATED in the result when it fires, so a wait that gives up says so
// rather than looking like a failed check. One wait replaces a sleep-poll
// loop of many turns with a single tool call and a single result.
//
// IT IS STILL Executes AND STILL GATED. It runs a shell command the model
// wrote, which can do anything local__bash can; it is auto-approved on the
// same footing as local__bash and not a loophole around the gate.
//
// proven-by: TestWait_ReturnsWhenTheCommandSucceeds
// proven-by: TestWait_PollsUntilSuccess
// proven-by: TestWait_GivesUpAtTheDeadlineAndSaysSo
// proven-by: TestWait_ReportsTheDeadlineInTheResult
// proven-by: TestWait_AnEmptyCommandIsRefused
// proven-by: TestWait_DeclaresItselfAsExecuting
func Wait(root string, timeout time.Duration) Tool {
	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	return Tool{
		Name:   "local__wait",
		Effect: Executes,
		Description: fmt.Sprintf("Wait for a condition by re-running a command until it "+
			"exits 0, then return. Use this instead of `sleep` in local__bash when you "+
			"are waiting for something to HAPPEN — a CI check to pass, a pod to go "+
			"Ready, a file to appear. Give the command that SUCCEEDS when the thing you "+
			"want has happened (e.g. a gh/kubectl check that exits non-zero until then); "+
			"the tool polls it and returns the instant it succeeds. It waits up to %s "+
			"and says so if it gives up, so you never sleep blind on a guessed duration.", timeout),
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{
		    "command":{"type":"string","description":"a shell command that exits 0 once the awaited condition holds, and non-zero until then"},
		    "reason":{"type":"string","description":"optional: what you are waiting for, shown at approval"}
		  },
		  "required":["command"]
		}`),
		Preview: func(args json.RawMessage) string {
			var in struct {
				Command string `json:"command"`
				Reason  string `json:"reason"`
			}
			if err := json.Unmarshal(args, &in); err != nil || in.Command == "" {
				return "(no command)"
			}
			if in.Reason != "" {
				return "wait until: " + in.Command + "\n(" + in.Reason + ")"
			}
			return "wait until: " + in.Command
		},
		Run: func(args json.RawMessage) (string, error) {
			var in struct {
				Command string `json:"command"`
				Reason  string `json:"reason"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("arguments are not valid JSON: %w", err)
			}
			if strings.TrimSpace(in.Command) == "" {
				return "", errors.New("command is required")
			}
			return waitForCommand(root, in.Command, timeout, defaultWaitInterval)
		},
	}
}

// waitForCommand polls command until it exits 0 or the deadline passes.
//
// THE DEADLINE IS THE WHOLE RUN, NOT EACH ATTEMPT. A single attempt that
// hangs does not outlast the wait: its children are killed the same way
// local__bash kills its own, and the overall context bounds the loop so a
// command that keeps failing still returns at the ceiling rather than
// running forever. // proven-by: TestWait_GivesUpAtTheDeadlineAndSaysSo
func waitForCommand(root, command string, timeout, interval time.Duration) (string, error) {
	dir, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("working directory is unreadable: %w", err)
	}

	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	attempts := 0
	var lastOut string
	for {
		attempts++
		out, ok := runOnce(ctx, dir, command)
		lastOut = out
		if ok {
			var b bytes.Buffer
			if s := strings.TrimRight(out, "\n"); s != "" {
				b.WriteString(s)
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "condition met after %s (%d attempt(s))",
				waitElapsed(timeout, deadline), attempts)
			return b.String(), nil
		}

		// No point sleeping if the next attempt could not finish before the
		// deadline anyway — report the give-up now rather than after a sleep.
		if time.Until(deadline) <= interval {
			break
		}
		select {
		case <-ctx.Done():
			// The deadline passed while the attempt was running.
			return giveUp(lastOut, timeout, attempts), fmt.Errorf("gave up after %s", timeout)
		case <-time.After(interval):
		}
	}
	return giveUp(lastOut, timeout, attempts), fmt.Errorf("gave up after %s", timeout)
}

// waitElapsed renders how long the wait took, rounded to the second.
func waitElapsed(timeout time.Duration, deadline time.Time) time.Duration {
	elapsed := timeout - time.Until(deadline)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Round(time.Second)
}

// giveUp renders the result for a wait that reached its deadline.
//
// SAYS THE DEADLINE OUT LOUD, which is the defect in local__bash this tool
// fixes: a wait that gives up tells the model it waited the full window and
// the condition still did not hold, so the model's next move is to
// investigate rather than to assume the check passed.
func giveUp(lastOut string, timeout time.Duration, attempts int) string {
	var b bytes.Buffer
	if s := strings.TrimRight(lastOut, "\n"); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "the condition did not hold within %s (%d attempt(s)); "+
		"the last attempt's output is above. The wait gave up — this is NOT a "+
		"passing result.", timeout, attempts)
	return b.String()
}

// runOnce runs the command once, killing its process group on cancellation
// exactly as runScript does, and reports whether it exited 0.
func runOnce(ctx context.Context, dir, command string) (string, bool) {
	cmd := exec.CommandContext(ctx, "sh") // #nosec G204 -- an approved command, gated by Registry.Run
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader([]byte(command))
	cmd.Env = os.Environ()
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
	runErr := cmd.Run()

	body := out.String()
	if len(body) > maxExecOutput {
		body = body[:maxExecOutput] + fmt.Sprintf("\n\n(output cut at %d bytes; there was more)", maxExecOutput)
	}
	return body, runErr == nil
}

// leadingSleep reports whether a script's first real command is `sleep`.
//
// WHY THE FIRST COMMAND AND NOT ANYWHERE. A `sleep` buried in the middle of
// a real script (retry backoff, pacing a loop) is legitimate and not what
// the nudge is for. The pattern the nudge targets is the sleep-then-poll
// the model reaches for in place of local__wait, which puts the sleep at
// the FRONT: `sleep 100; gh pr checks ...`. Matching only the leading
// position catches that shape and leaves honest uses alone.
//
// proven-by: TestLeadingSleep_MatchesALeadingSleep
// proven-by: TestLeadingSleep_IgnoresASleepThatIsNotFirst
// proven-by: TestLeadingSleep_IgnoresAScriptWithNoSleep
func leadingSleep(script string) bool {
	return leadingSleepRe.MatchString(strings.TrimLeft(script, " \t\n"))
}

// A leading `sleep <n>`, allowing comment lines before it (the model often
// prefixes `# waiting for ...`), then `sleep` as the first real token.
var leadingSleepRe = regexp.MustCompile(`(?s)^\s*(#[^\n]*\n\s*)*sleep\s`)

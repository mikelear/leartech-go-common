package agentloop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
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

// MaxWaitTimeout is the hard ceiling an override may ask for.
//
// A PER-CALL OVERRIDE, BOUNDED. A model that knows a check will take longer
// than the default (a slow end-to-end suite) may ask for more time, and one
// that expects a fast result may ask for less so a wait that is really an
// error gives up sooner instead of sitting idle to the default. Both are the
// model's call to make, so the timeout is an optional argument. The ceiling
// is here so an override does not turn a bounded wait into an unbounded one:
// anything above it is clamped down to it and said so. // proven-by: TestWait_ClampsAnOverTheCeilingTimeout
const MaxWaitTimeout = 60 * time.Minute

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
	return WaitIn(root, timeout, ExecOpts{})
}

// WaitIn is Wait with the extra context a running wait announces to the
// ExecRegistry, so a second terminal can `ship-proven exec list` / `exec kill`
// it. Wait is WaitIn with an empty ExecOpts, leaving every existing caller and
// test untouched.
//
// proven-by: TestWaitIn_AnnouncesEachAttemptToTheRegistry
func WaitIn(root string, timeout time.Duration, opts ExecOpts) Tool {
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
			"by default and says so if it gives up, so you never sleep blind on a "+
			"guessed duration. Pass timeout_seconds to override that default — more "+
			"for a check you know is slow, less for one that should be quick so a "+
			"wait that is really an error gives up sooner (capped at %s).", timeout, MaxWaitTimeout),
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{
		    "command":{"type":"string","description":"a shell command that exits 0 once the awaited condition holds, and non-zero until then"},
		    "reason":{"type":"string","description":"optional: what you are waiting for, shown at approval"},
		    "timeout_seconds":{"type":"integer","description":"optional: override the default wait ceiling (seconds). Raise it for a check you know is slow, lower it for one that should finish quickly so a wait that is really an error gives up sooner. Clamped to a hard maximum."}
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
				Command        string `json:"command"`
				Reason         string `json:"reason"`
				TimeoutSeconds int    `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("arguments are not valid JSON: %w", err)
			}
			if strings.TrimSpace(in.Command) == "" {
				return "", errors.New("command is required")
			}
			// The model may override the default ceiling per call; clampWaitTimeout
			// resolves the effective deadline and any note explaining a clamp, so an
			// override that was reduced to the maximum does not pass silently.
			effective, clampNote := clampWaitTimeout(in.TimeoutSeconds, timeout)
			cmd, note := stripLeadingSleep(in.Command)
			out, err := waitForCommand(root, cmd, effective, defaultWaitInterval, opts)
			for _, n := range []string{clampNote, note} {
				if n != "" {
					out = n + "\n\n" + out
				}
			}
			return out, err
		},
	}
}

// clampWaitTimeout resolves the deadline for one wait from an optional
// per-call override, and returns a note when it had to change what was asked.
//
// WHAT IT DECIDES. Zero (or negative) means "no override" — the built-in
// default for this tool is used, with no note. A positive value is taken as
// seconds and used as the deadline, so a model that knows better than the
// default can say so. Anything above MaxWaitTimeout is CLAMPED DOWN to the
// ceiling and a note explains it, because a silent clamp would leave the model
// reasoning on a deadline that is not the one that ran — the same failure the
// whole tool exists to end.
//
// proven-by: TestClampWaitTimeout_ZeroUsesTheDefault
// proven-by: TestClampWaitTimeout_UsesAPositiveOverride
// proven-by: TestClampWaitTimeout_ClampsAboveTheCeilingAndSaysSo
// proven-by: TestWait_HonoursATimeoutOverride
// proven-by: TestWait_ClampsAnOverTheCeilingTimeout
func clampWaitTimeout(seconds int, def time.Duration) (effective time.Duration, note string) {
	if seconds <= 0 {
		return def, ""
	}
	want := time.Duration(seconds) * time.Second
	if want > MaxWaitTimeout {
		return MaxWaitTimeout, fmt.Sprintf("[shell] requested wait timeout %s exceeds the maximum %s; "+
			"clamped to %s.", want, MaxWaitTimeout, MaxWaitTimeout)
	}
	return want, ""
}

// waitForCommand polls command until it exits 0 or the deadline passes.
//
// ONE CHILD FOR THE WHOLE WAIT, NOT ONE PER ATTEMPT. The poll loop — run the
// command, and if it fails sleep the interval and run it again until success
// or the deadline — is itself a shell script run as a SINGLE child process
// (see waitLoopScript). That is what makes a wait killable from another
// terminal: it is ONE process, in the ExecRegistry for the WHOLE wait (not
// just during an attempt, with nothing listed during the 5s gaps), and
// `ship-proven exec kill` of that one pid ends the whole wait. The earlier
// design polled with one child per attempt and did the sleeping in Go, so for
// most of the wait there was no process to list or kill — the defect this
// fixes.
//
// THE OUTCOME IS READ FROM THE CHILD'S EXIT. The loop exits 0 the instant the
// command succeeds, 97 when it reaches the deadline (a sentinel distinct from
// any ordinary exit code), and is killed-by-signal when stopped from outside —
// the same three cases the caller already tells apart, now for the loop rather
// than a single attempt.
//
// proven-by: TestWait_ReturnsWhenTheCommandSucceeds
// proven-by: TestWait_PollsUntilSuccess
// proven-by: TestWait_GivesUpAtTheDeadlineAndSaysSo
// proven-by: TestWait_IsOneKillableChildForTheWholeWait
func waitForCommand(root, command string, timeout, interval time.Duration, opts ExecOpts) (string, error) {
	dir, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("working directory is unreadable: %w", err)
	}

	deadline := time.Now().Add(timeout)
	// The whole loop runs under one timeout, with a small grace so the script
	// reaches its own deadline branch (exit 97) and prints the last attempt's
	// output before the context would hard-kill it.
	ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Second)
	defer cancel()

	script := waitLoopScript(command, timeout, interval)
	out, runErr := runInGroup(ctx, dir, script, "local__wait", scriptSummary(command), opts)
	if len(out) > maxExecOutput {
		out = out[:maxExecOutput] + fmt.Sprintf("\n\n(output cut at %d bytes; there was more)", maxExecOutput)
	}

	// OUR OWN DEADLINE FIRST, exactly as local__bash checks it before
	// killedBySignal — because this is the defect this branch fixes. The loop
	// script is supposed to reach its own exit-97 branch within the grace
	// window; when it does not (it was mid-`sleep` on a long poll interval,
	// say), the outer context fires and SIGKILLs the group. That SIGKILL is a
	// signalled death and WOULD otherwise be read by killedBySignal below as a
	// person running `ship-proven exec kill` — reporting a plain timeout as a
	// deliberate external kill, and telling the model to stop and wait for a
	// human who was not there. Checking the context's own DeadlineExceeded
	// first routes it to giveUp, where it belongs. // proven-by: TestWait_AnOwnDeadlineIsNotReportedAsAnExternalKill
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return giveUp(out, timeout), fmt.Errorf("gave up after %s", timeout)
	}

	// Killed from outside: the whole wait was stopped by `ship-proven exec
	// kill`. Reported as deliberate, exactly as local__bash does, so the model
	// stops rather than treating it as a failed check. // proven-by: TestWait_AnExternalKillIsReportedAsDeliberate
	if sig, ok := killedBySignal(runErr); ok {
		var b bytes.Buffer
		if s := strings.TrimRight(out, "\n"); s != "" {
			b.WriteString(s)
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "this wait was killed from outside (signal: %s) — a person "+
			"stopped it with `ship-proven exec kill`. This is NOT a failed check; do "+
			"not retry or work around it. Stop and wait for them to tell you why.", sig)
		return b.String(), fmt.Errorf("killed from outside (signal: %s)", sig)
	}

	if runErr == nil {
		// The loop exited 0: the command succeeded.
		var b bytes.Buffer
		if s := strings.TrimRight(out, "\n"); s != "" {
			b.WriteString(s)
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "condition met after %s", waitElapsed(timeout, deadline))
		return b.String(), nil
	}

	// Any non-zero exit (the deadline sentinel 97, or the context's own kill)
	// is a give-up: the condition did not hold in time.
	return giveUp(out, timeout), fmt.Errorf("gave up after %s", timeout)
}

// waitLoopScript builds the shell poll loop that IS the wait: run the user's
// command, and while it fails, sleep the interval and run it again, until it
// succeeds (exit 0) or the deadline passes (exit 97).
//
// POSIX sh, no bashisms, so it runs under the same /bin/sh every tool uses.
// The user command is embedded verbatim (the caller already stripped a leading
// sleep and gated the whole thing through Registry.Run), run with `sh -c` so a
// pipeline or && chain behaves as written.
//
// proven-by: TestWaitLoopScript_SucceedsImmediatelyOnAPassingCommand
// proven-by: TestWaitLoopScript_ExitsWithTheDeadlineSentinelOnTimeout
func waitLoopScript(command string, timeout, interval time.Duration) string {
	secs := int(timeout.Seconds())
	if secs < 1 {
		secs = 1
	}
	ivl := int(interval.Seconds())
	if ivl < 1 {
		ivl = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "__end=$(( $(date +%%s) + %d ))\n", secs)
	b.WriteString("while :; do\n")
	b.WriteString("  if sh -c " + shSingleQuote(command) + "; then exit 0; fi\n")
	b.WriteString("  if [ \"$(date +%s)\" -ge \"$__end\" ]; then exit 97; fi\n")
	fmt.Fprintf(&b, "  sleep %d\n", ivl)
	b.WriteString("done\n")
	return b.String()
}

// shSingleQuote wraps a string in single quotes for POSIX sh, escaping any
// embedded single quotes, so the user's command is passed to `sh -c` exactly
// as written however it is quoted.
//
// proven-by: TestShSingleQuote_WrapsAndEscapes
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
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
func giveUp(lastOut string, timeout time.Duration) string {
	var b bytes.Buffer
	if s := strings.TrimRight(lastOut, "\n"); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "the condition did not hold within %s; the last attempt's "+
		"output is above. The wait gave up — this is NOT a passing result.", timeout)
	return b.String()
}

// stripLeadingSleep removes a pointless `sleep N;` from the FRONT of a wait
// command and returns a note saying it did.
//
// WHY. local__wait already re-runs the command every few seconds until it
// succeeds, so a model that writes `sleep 100; gh pr checks ...` as the wait
// command pays the 100s on the FIRST attempt for nothing — the poll loop is
// the wait. Observed live: a model moved from sleep-in-bash to
// sleep-prefixed-wait, which is harmless but slower and misleading. Stripping
// the prefix makes the first poll immediate and tells the model why, so the
// habit corrects rather than persists.
//
// ONLY A LEADING sleep, and only the sleep itself: a `sleep` inside the real
// command (a retry backoff within an until-loop) is left untouched, because
// that one is doing a job the poll interval does not.
//
// proven-by: TestStripLeadingSleep_RemovesALeadingSleepAndExplains
// proven-by: TestStripLeadingSleep_LeavesACommandWithoutALeadingSleep
// proven-by: TestStripLeadingSleep_LeavesASleepInsideTheCommand
func stripLeadingSleep(command string) (stripped, note string) {
	m := leadingSleepPrefixRe.FindString(command)
	if m == "" {
		return command, ""
	}
	rest := strings.TrimLeft(command[len(m):], " \t")
	if rest == "" {
		// The whole command was just a sleep — nothing to wait ON. Leave it;
		// waitForCommand will run it and succeed, which is the caller's intent
		// however odd, and stripping to empty would be a different command.
		return command, ""
	}
	return rest, "[shell] dropped a leading `" + strings.TrimSpace(m) +
		"` — local__wait re-runs the command until it succeeds, so sleeping first only delays the first check."
}

// A leading `sleep <n>` followed by `;` or `&&` and more command. Anchored at
// the start (after optional whitespace); the separator is required, so a bare
// `sleep 5` with nothing after it does not match (handled as "the whole
// command is a sleep" above).
var leadingSleepPrefixRe = regexp.MustCompile(`^\s*sleep\s+[0-9.]+[a-z]*\s*(;|&&)\s*`)

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

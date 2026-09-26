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
	"syscall"
	"time"
)

// DefaultExecTimeout bounds one script.
//
// A hung command would otherwise hang the run with no prompt and no way
// back. // proven-by: TestBash_ATimeoutIsReportedNotHung
// In the transcript that reads the same as a crash. // proven-by: TestBash_ATimeoutIsReportedNotHung
const DefaultExecTimeout = 2 * time.Minute

// maxExecOutput bounds what comes back.
//
// The output goes into the next prompt and is paid for by the token, so a
// script that prints a megabyte is a bill rather than an answer.
const maxExecOutput = 64 * 1024

// Bash runs a script the model wrote.
//
// THE MOST DANGEROUS TOOL HERE BY A LONG WAY, and the design says so:
// Executes is denied by default, and reaches Ask only when an operator
// passed --allow-exec. Nothing about this tool decides that; the gate in
// Registry.Run does, which is the point of having moved it there.
//
// A SCRIPT, NOT A COMMAND AND ARGUMENTS. The model already writes shell;
// pretending otherwise with an argv array buys no safety, because `sh -c`
// is one element away, and it costs the operator the ability to read what
// they are approving as a single piece of text.
//
// proven-by: TestBash_RunsAScriptAndReturnsItsOutput
// proven-by: TestBash_RunsInTheRootNotTheProcessDirectory
// proven-by: TestBash_AFailingScriptReturnsItsOutputAndStatus
// proven-by: TestBash_ATimeoutIsReportedNotHung
// proven-by: TestBash_LargeOutputIsCappedAndSaysSo
// proven-by: TestBash_AnEmptyScriptIsRefused
func Bash(root string, timeout time.Duration) Tool {
	if timeout <= 0 {
		timeout = DefaultExecTimeout
	}
	return Tool{
		Name:   "local__bash",
		Effect: Executes,
		Description: "Run a shell script in the working directory. The whole " +
			"script is shown to the operator for approval before it runs, so " +
			"write it to be read: one purpose, no surprises.",
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{"script":{"type":"string","description":"the shell script to run"}},
		  "required":["script"]
		}`),
		Preview: func(args json.RawMessage) string {
			if s := ScriptOf(args); s != "" {
				return s
			}
			return "(no script)"
		},
		Run: func(args json.RawMessage) (string, error) {
			var in struct {
				Script string `json:"script"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("arguments are not valid JSON: %w", err)
			}
			if in.Script == "" {
				return "", errors.New("script is required")
			}
			return runScript(root, in.Script, timeout)
		},
	}
}

// ScriptOf pulls the script out of a call's arguments, for a UI that has
// to show an operator what they are approving.
//
// Rendering the raw JSON instead would show the script escaped onto one
// line, unreadable exactly when reading it matters. // proven-by: TestScriptOf_ReturnsTheScriptForDisplay
//
// proven-by: TestScriptOf_ReturnsTheScriptForDisplay
// proven-by: TestScriptOf_IsEmptyForAnythingElse
func ScriptOf(args json.RawMessage) string {
	var in struct {
		Script string `json:"script"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ""
	}
	return in.Script
}

func runScript(root, script string, timeout time.Duration) (string, error) {
	dir, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("working directory is unreadable: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Passed on stdin rather than as -c, so a script containing quotes,
	// newlines or a NUL-adjacent oddity arrives exactly as approved. The
	// bytes that run are the bytes that were shown.
	cmd := exec.CommandContext(ctx, "sh") // #nosec G204 -- an approved script, gated by Registry.Run
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader([]byte(script))
	cmd.Env = os.Environ()

	// THE TIMEOUT KILLS THE WHOLE PROCESS GROUP, NOT JUST sh.
	//
	// Measured: `sleep 30` with a 300ms timeout took the full 30 seconds.
	// CommandContext kills the shell, but the shell's children survive,
	// inherit the output pipe, and Run blocks until the last writer
	// closes it — the hang the timeout was added to prevent. // proven-by: TestBash_ATimeoutKillsTheScriptsChildrenToo
	//
	// Setpgid puts the script in its own group so the whole tree can be
	// killed; WaitDelay is the backstop for anything that survives even
	// that, forcing the pipes shut rather than waiting forever.
	//
	// proven-by: TestBash_ATimeoutIsReportedNotHung
	// proven-by: TestBash_ATimeoutKillsTheScriptsChildrenToo
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
	truncated := false
	if len(body) > maxExecOutput {
		body, truncated = body[:maxExecOutput], true
	}

	var b bytes.Buffer
	b.WriteString(body)
	if truncated {
		// SAID, NOT SILENT. A model shown half the output believes it saw
		// all of it and concludes the rest did not happen.
		fmt.Fprintf(&b, "\n\n(output cut at %d bytes; there was more)", maxExecOutput)
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(&b, "\n\nthe script was killed after %s", timeout)
		return b.String(), fmt.Errorf("timed out after %s", timeout)
	}
	if runErr != nil {
		// THE OUTPUT COMES BACK WITH THE FAILURE. A non-zero exit whose
		// output was discarded tells the model only that something went
		// wrong, and its next move is to run it again to find out what.
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			fmt.Fprintf(&b, "\n\nexit status %d", ee.ExitCode())
			return b.String(), fmt.Errorf("exit status %d", ee.ExitCode())
		}
		return b.String(), runErr
	}
	if b.Len() == 0 {
		// Not an empty string, which reads the same as a broken tool. // proven-by: TestBash_SilenceIsReportedAsSilence
		return "(the script produced no output and exited 0)", nil
	}
	return b.String(), nil
}

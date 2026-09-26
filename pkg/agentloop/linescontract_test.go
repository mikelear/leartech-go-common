package agentloop

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/aigateway"
)

// scriptedLines returns a fixed sequence, then whatever error is set.
//
// A HAND-WRITTEN Lines RATHER THAN A PIPE, because the point of these
// tests is the two ERRORS a Lines may return, and a pipe can only ever
// produce io.EOF. The terminal implementation that produces
// ErrInterrupted is not in this package — so without a fake, the loop's
// side of that contract is untestable here, which is exactly how it came
// to be documented against a test in another repository.
type scriptedLines struct {
	lines  []string
	err    error
	closed bool
}

func (s *scriptedLines) ReadLine() (string, error) {
	if len(s.lines) > 0 {
		l := s.lines[0]
		s.lines = s.lines[1:]
		return l, nil
	}
	if s.err != nil {
		return "", s.err
	}
	return "", io.EOF
}

func (s *scriptedLines) Close() error { s.closed = true; return nil }

// countingGateway answers every turn identically and counts the calls.
func countingGateway(n int) *fakeGateway {
	turns := make([][]aigateway.StreamChunk, 0, n)
	for range n {
		turns = append(turns, []aigateway.StreamChunk{text("ok"), done("stop")})
	}
	return &fakeGateway{turns: turns}
}

// ErrInterrupted ABANDONS THE LINE AND KEEPS THE RUN. That distinction is
// the reason the sentinel exists at all: a run that quit on it would take
// the operator's half-typed line with it, and one that ignored it would
// send the abandoned line to a model.
func TestRunner_AnInterruptedLineDoesNotEndTheRun(t *testing.T) {
	// Interrupt arrives, then the caller carries on and asks something.
	lines := &interruptThenAsk{ask: "what is it?"}
	fg := countingGateway(1)

	r := &Runner{Model: "m", Client: fg, Tools: NewRegistry(), Out: io.Discard}
	if err := r.Run(context.Background(), lines, "", 5); err != nil {
		t.Fatalf("an interrupt must not fail the run: %v", err)
	}
	if len(fg.seen) != 1 {
		t.Fatalf("the gateway saw %d turn(s), want 1: the interrupted line must be "+
			"thrown away and the next one still reach the model", len(fg.seen))
	}
	// The abandoned text never left the process.
	for _, req := range fg.seen {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "half typed") {
				t.Error("the abandoned line was sent to the model")
			}
		}
	}
}

// interruptThenAsk returns an interrupt first, then one real line, then EOF.
type interruptThenAsk struct {
	stage int
	ask   string
}

func (i *interruptThenAsk) ReadLine() (string, error) {
	i.stage++
	switch i.stage {
	case 1:
		// A terminal reports the abandoned text as "" with the sentinel; the
		// text itself is what the operator typed and discarded.
		return "half typed", ErrInterrupted
	case 2:
		return i.ask, nil
	default:
		return "", io.EOF
	}
}

func (i *interruptThenAsk) Close() error { return nil }

// io.EOF ENDS THE RUN, which is the other half of the same contract. If
// both did the same thing there would be no reason for the sentinel.
func TestRunner_EOFEndsTheRun(t *testing.T) {
	fg := countingGateway(1)
	r := &Runner{Model: "m", Client: fg, Tools: NewRegistry(), Out: io.Discard}

	if err := r.Run(context.Background(), &scriptedLines{err: io.EOF}, "", 5); err != nil {
		t.Fatalf("EOF is how a run ends, not how it fails: %v", err)
	}
	if len(fg.seen) != 0 {
		t.Errorf("the gateway saw %d turn(s) after an immediate EOF", len(fg.seen))
	}
}

// AN UNEXPECTED READ FAILURE IS NOT A CLEAN END. A closed pipe and a
// broken one are different facts, and treating the second as the first
// would report success for a run whose input was lost.
func TestRunner_AReadFailureIsNotACleanEnd(t *testing.T) {
	boom := errors.New("input device disappeared")
	r := &Runner{Model: "m", Client: countingGateway(1), Tools: NewRegistry(), Out: io.Discard}

	err := r.Run(context.Background(), &scriptedLines{err: boom}, "", 5)
	if err == nil {
		t.Fatal("a broken input must not look like a finished run")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the read failure carried through", err)
	}
}

// INTERCEPT GETS THE LINE BEFORE THE MODEL DOES, so a consumer can own
// slash commands without this package learning what a command is.
//
// THE CLAIM IS ON THIS PACKAGE'S FIELD, so the proof belongs here. It was
// documented against a test in the consumer, which meant deleting the
// Intercept call entirely would have left this package's suite green.
func TestRunner_AnInterceptedLineNeverReachesTheModel_Library(t *testing.T) {
	fg := countingGateway(2)
	var intercepted []string

	r := &Runner{Model: "m", Client: fg, Tools: NewRegistry(), Out: io.Discard,
		Intercept: func(line string) (bool, error) {
			if strings.HasPrefix(line, "/") {
				intercepted = append(intercepted, line)
				return true, nil
			}
			return false, nil
		}}

	lines := &scriptedLines{lines: []string{"/help", "a real question"}}
	if err := r.Run(context.Background(), lines, "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(intercepted) != 1 || intercepted[0] != "/help" {
		t.Errorf("intercepted = %v, want [/help]", intercepted)
	}
	if len(fg.seen) != 1 {
		t.Fatalf("the gateway saw %d turn(s), want 1 — the intercepted line "+
			"reached the model", len(fg.seen))
	}
	for _, m := range fg.seen[0].Messages {
		if strings.Contains(m.Content, "/help") {
			t.Error("the intercepted line was sent to the model anyway")
		}
	}
}

// AN INTERCEPT THAT FAILS STOPS THE RUN, rather than being swallowed and
// leaving the operator's command silently undone.
func TestRunner_AFailingInterceptStopsTheRun(t *testing.T) {
	boom := errors.New("/deploy is not allowed here")
	r := &Runner{Model: "m", Client: countingGateway(1), Tools: NewRegistry(), Out: io.Discard,
		Intercept: func(string) (bool, error) { return true, boom }}

	err := r.Run(context.Background(), &scriptedLines{lines: []string{"/deploy"}}, "", 5)
	if err == nil || !errors.Is(err, boom) {
		t.Errorf("err = %v, want the intercept's failure", err)
	}
}

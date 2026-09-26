package agentloop

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPipeLines_ReadsLinesAndEndsAtEOF(t *testing.T) {
	p := NewPipeLines(strings.NewReader("one\ntwo\n"))
	for _, want := range []string{"one", "two"} {
		got, err := p.ReadLine()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if got != want {
			t.Errorf("line = %q, want %q", got, want)
		}
	}
	if _, err := p.ReadLine(); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF at the end", err)
	}
}

// PipeLines NEVER RETURNS ErrInterrupted. There is no terminal to press
// Ctrl-C on, and a pipe that invented an interrupt would make an
// unattended run discard a line for no reason.
func TestPipeLines_NeverInterrupts(t *testing.T) {
	p := NewPipeLines(strings.NewReader("only line\n"))
	for {
		_, err := p.ReadLine()
		if errors.Is(err, ErrInterrupted) {
			t.Fatal("a pipe reported an interrupt; nothing can press Ctrl-C on it")
		}
		if err != nil {
			break
		}
	}
}

// Close IS SAFE AND DOES NOT CLOSE THE UNDERLYING READER. The caller owns
// the file it opened, and a Lines that closed someone else's stdin would
// take the rest of the process with it.
func TestPipeLines_CloseLeavesTheReaderAlone(t *testing.T) {
	r := &closeCounter{Reader: strings.NewReader("x\n")}
	p := NewPipeLines(r)
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if r.closes != 0 {
		t.Errorf("the underlying reader was closed %d time(s); the caller owns it", r.closes)
	}
}

type closeCounter struct {
	io.Reader
	closes int
}

func (c *closeCounter) Close() error { c.closes++; return nil }

func TestQuiet_ShowsNothingThroughEveryMethod(t *testing.T) {
	var q UI = Quiet{}
	// If any of these wrote anywhere, it could only be to a global, which
	// is the thing being ruled out.
	q.TurnStarted()
	q.FirstOutput()
	q.ToolStarted("local__read_file", []byte(`{"path":"x"}`))
	q.ToolFinished("local__read_file", true, 10, "")
	q.ToolLinks([]string{"https://github.com/x/y/pull/1"})
	q.Note("%s", "anything")
	q.Highlight("%s", "anything")
	q.Progress("%s", "transient")
}

// A nil UI IS Quiet, NOT A PANIC. An unattended caller that sets no UI is
// the common case, and the loop calls into it on every turn and every tool
// — so a nil there would crash the run rather than render nothing.
func TestRunner_ANilUIIsQuietRatherThanAPanic(t *testing.T) {
	fg := countingGateway(1)
	r := &Runner{Model: "m", Client: fg, Tools: NewRegistry(), Out: io.Discard}
	if r.UI != nil {
		t.Fatal("the fixture must start with no UI for this to mean anything")
	}
	if err := r.Run(t.Context(), NewPipeLines(strings.NewReader("hello\n")), "", 5); err != nil {
		t.Fatalf("a run with no UI: %v", err)
	}
	if r.UI == nil {
		t.Error("UI was left nil; every later call would dereference it")
	}
}

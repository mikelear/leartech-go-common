package agentloop

import (
	"bufio"
	"errors"
	"io"
)

// Lines is where a turn comes from.
//
// ONE INTERFACE FOR BOTH MODES, so a scripted run and a person at a
// terminal reach the Runner by the same route. The agreed line is that
// rendering and nondeterminism may differ under --script; decisions may
// not, and an input source is not a decision.
type Lines interface {
	// ReadLine returns the next input. io.EOF ends the session;
	// ErrInterrupted abandons the line and asks for another.
	ReadLine() (string, error)
	Close() error
}

// ErrInterrupted means: throw this line away, keep the run going.
//
// SEPARATE FROM io.EOF, WHICH ENDS THE RUN. That distinction is the whole
// reason the sentinel exists, and it is a contract of this package rather
// than a detail of any one Lines: whoever implements ReadLine decides how
// an abandoned line is signalled, and this says what the loop will do
// about it. // proven-by: TestRunner_AnInterruptedLineDoesNotEndTheRun
//
// The evidence for WHY a terminal needs it lives with the terminal
// implementation, which is not in this package: x/term reports Ctrl-C and
// Ctrl-D identically, so acting on io.EOF directly would quit the session
// when someone pressed Ctrl-C to clear a half-typed line.
//
// proven-by: TestRunner_AnInterruptedLineDoesNotEndTheRun
// proven-by: TestRunner_EOFEndsTheRun
var ErrInterrupted = errors.New("agentloop: interrupted")

// PipeLines reads from a script or a pipe: no editing, no history, no TTY.
//
// proven-by: TestPipeLines_ReadsLinesAndEndsAtEOF
type PipeLines struct {
	sc *bufio.Scanner
}

// NewPipeLines reads whole lines from r.
func NewPipeLines(r io.Reader) *PipeLines {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &PipeLines{sc: sc}
}

// ReadLine returns the next line, or io.EOF at the end.
func (p *PipeLines) ReadLine() (string, error) {
	if !p.sc.Scan() {
		if err := p.sc.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return p.sc.Text(), nil
}

// Close does nothing: a pipe owns no terminal state to restore.
func (p *PipeLines) Close() error { return nil }

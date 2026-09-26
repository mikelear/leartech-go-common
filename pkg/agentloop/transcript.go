package agentloop

import (
	"encoding/json"
	"io"
)

// TranscriptEvent is one machine-readable line of what the loop did.
//
// WHY THIS EXISTS RATHER THAN SCRAPING STDOUT. The shell's stdout is prose
// from a model. A test that greps it breaks whenever wording changes, and —
// worse — passes when the model says something plausible and wrong. The
// suite becomes one nobody trusts and everybody reruns.
//
// So a run writes twice: prose for a human, and this for a test. A
// scripted run asserts on STRUCTURE rather than wording.
//
// proven-by: TestTranscript_RecordsStructureNotProse
// proven-by: TestTranscript_TextIsCountedNotQuoted
type TranscriptEvent struct {
	Kind string `json:"kind"`

	// State is where the loop was after the action. Named so a test can
	// assert the machine reached idle rather than inferring it.
	State string `json:"state,omitempty"`

	// Tool and Args describe a call. Args is the model's payload, kept raw.
	Tool string          `json:"tool,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`

	// Chars is the LENGTH of shown text, not the text.
	//
	// Deliberate: a transcript carrying model prose would tempt a test to
	// assert on it, which is the brittleness this file exists to avoid. It
	// also keeps a machine-readable artefact free of whatever the model
	// said, which may be anything.
	Chars int `json:"chars,omitempty"`

	// Finish and Error close a turn.
	Finish string `json:"finish,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Transcript writes one JSON object per line.
type Transcript struct {
	w   io.Writer
	enc *json.Encoder
}

// NewTranscript returns a Transcript, or nil when w is nil.
//
// A nil Transcript is usable and does nothing, so the interactive path
// carries no branch — the alternative is `if t != nil` at every call site,
// which is where one gets forgotten.
func NewTranscript(w io.Writer) *Transcript {
	if w == nil {
		return nil
	}
	return &Transcript{w: w, enc: json.NewEncoder(w)}
}

// Record writes what an action did.
//
// proven-by: TestTranscript_ANilTranscriptIsSafe
func (t *Transcript) Record(a Action, after State) error {
	if t == nil {
		return nil
	}
	e := TranscriptEvent{State: after.String()}
	switch a.Kind {
	case SendTurn:
		e.Kind = "send_turn"
	case RunTool:
		e.Kind = "tool_call"
		if a.Call != nil {
			e.Tool, e.Args = a.Call.Name, a.Call.Arguments
		}
	case ShowText:
		e.Kind, e.Chars = "text", len(a.Text)
	case ShowReasoning:
		e.Kind, e.Chars = "reasoning", len(a.Text)
	case ShowError:
		e.Kind = "error"
		if a.Err != nil {
			e.Error = a.Err.Error()
		}
	case AwaitInput:
		e.Kind = "await_input"
	}
	return t.enc.Encode(e)
}

// RecordToolResult notes what a tool returned, which is not an Action —
// the loop is told, it does not decide it.
//
// proven-by: TestTranscript_AFailedToolIsDistinguishableFromASilentOne
func (t *Transcript) RecordToolResult(name string, ok bool, result string) error {
	if t == nil {
		return nil
	}
	e := TranscriptEvent{Kind: "tool_result", Tool: name, Chars: len(result)}
	if !ok {
		// A failed tool that recorded only its length would be
		// indistinguishable from one that returned nothing, and "the tool
		// ran and said nothing" is a different bug from "the tool failed".
		e.Error = "tool reported failure"
	}
	return t.enc.Encode(e)
}

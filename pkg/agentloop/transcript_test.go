package agentloop

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func decode(t *testing.T, buf *bytes.Buffer) []TranscriptEvent {
	t.Helper()
	var out []TranscriptEvent
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var e TranscriptEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("transcript line is not JSON: %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

// The transcript records what happened, structurally.
//
// This is what a shell-script test asserts on. Scraping stdout would break
// on rewording and pass on plausible-but-wrong prose.
func TestTranscript_RecordsStructureNotProse(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf)

	_ = tr.Record(Action{Kind: RunTool, Call: &ToolCall{
		Name: "local__read_file", Arguments: json.RawMessage(`{"path":"/etc/hosts"}`),
	}}, Running)
	_ = tr.Record(Action{Kind: AwaitInput}, Idle)

	got := decode(t, &buf)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Kind != "tool_call" || got[0].Tool != "local__read_file" {
		t.Errorf("event 0 = %+v", got[0])
	}
	// The arguments survive intact — a test asserting WHICH file was read
	// needs them, and they are the model's payload, not prose.
	var args map[string]string
	if err := json.Unmarshal(got[0].Args, &args); err != nil {
		t.Fatalf("args did not survive: %v", err)
	}
	if args["path"] != "/etc/hosts" {
		t.Errorf("args = %v", args)
	}
	// The state is named, so a test asserts the machine arrived rather than
	// inferring it from the absence of later events.
	if got[1].State != "idle" {
		t.Errorf("final state = %q, want idle", got[1].State)
	}
}

// Shown text is counted, never quoted.
//
// A transcript carrying model prose would tempt a test to assert on it,
// which is the brittleness this design exists to avoid.
func TestTranscript_TextIsCountedNotQuoted(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf)
	_ = tr.Record(Action{Kind: ShowText, Text: "the capital of France is Paris"}, Awaiting)

	if strings.Contains(buf.String(), "Paris") {
		t.Errorf("the transcript quoted model prose:\n%s", buf.String())
	}
	got := decode(t, &buf)
	if got[0].Kind != "text" || got[0].Chars != 30 {
		t.Errorf("got %+v, want text of 30 chars", got[0])
	}
}

// A failed tool is distinguishable from one that returned nothing.
func TestTranscript_AFailedToolIsDistinguishableFromASilentOne(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf)
	_ = tr.RecordToolResult("local__read_file", false, "")
	_ = tr.RecordToolResult("local__read_file", true, "")

	got := decode(t, &buf)
	if got[0].Error == "" {
		t.Error("a failed tool recorded no error; it reads as one that said nothing")
	}
	if got[1].Error != "" {
		t.Error("a successful empty result was recorded as a failure")
	}
}

// A nil transcript is usable and silent.
//
// So the interactive path carries no branch. `if t != nil` at every call
// site is where one gets forgotten.
func TestTranscript_ANilTranscriptIsSafe(t *testing.T) {
	var tr *Transcript
	if err := tr.Record(Action{Kind: AwaitInput}, Idle); err != nil {
		t.Errorf("nil Record returned %v", err)
	}
	if err := tr.RecordToolResult("x", true, "y"); err != nil {
		t.Errorf("nil RecordToolResult returned %v", err)
	}
}

// An error action carries its reason.
func TestTranscript_AnErrorCarriesItsReason(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf)
	_ = tr.Record(Action{Kind: ShowError, Err: errors.New("budget exceeded")}, Idle)

	got := decode(t, &buf)
	if !strings.Contains(got[0].Error, "budget exceeded") {
		t.Errorf("got %+v, want the reason recorded", got[0])
	}
}

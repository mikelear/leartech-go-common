package agentloop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/aigateway"
)

// fakeGateway replays scripted turns, one per call.
//
// A fake rather than a live gateway: the failures that matter here — a
// truncated stream, a tool call the registry does not know — cannot be
// produced on demand against a real supplier, and every run would cost
// money.
type fakeGateway struct {
	turns [][]aigateway.StreamChunk
	seen  []aigateway.ChatRequest

	// errs refuses the nth call outright, the way a 401 or a dead backend
	// does — before any chunk exists to carry the failure.
	errs []error
}

func (f *fakeGateway) ChatStream(_ context.Context, req aigateway.ChatRequest) (<-chan aigateway.StreamChunk, error) {
	f.seen = append(f.seen, req)
	if len(f.errs) > 0 {
		var e error
		e, f.errs = f.errs[0], f.errs[1:]
		if e != nil {
			return nil, e
		}
	}
	ch := make(chan aigateway.StreamChunk)
	var turn []aigateway.StreamChunk
	if len(f.turns) > 0 {
		turn, f.turns = f.turns[0], f.turns[1:]
	}
	go func() {
		defer close(ch)
		for _, c := range turn {
			ch <- c
		}
	}()
	return ch, nil
}

func text(s string) aigateway.StreamChunk {
	return aigateway.StreamChunk{Kind: aigateway.StreamText, Text: s}
}
func done(finish string) aigateway.StreamChunk {
	return aigateway.StreamChunk{Kind: aigateway.StreamDone, Finish: finish}
}
func toolCall(id, name, args string) aigateway.StreamChunk {
	return aigateway.StreamChunk{Kind: aigateway.StreamToolCall, ToolCall: &aigateway.ToolCall{
		ID: id, Name: name, Arguments: json.RawMessage(args),
	}}
}

func runWith(t *testing.T, fg *fakeGateway, reg *Registry, input string) (string, []TranscriptEvent) {
	t.Helper()
	var out, tr bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: reg,
		Out: &out, Transcript: NewTranscript(&tr)}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader(input)), "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}
	var evs []TranscriptEvent
	for _, line := range strings.Split(strings.TrimSpace(tr.String()), "\n") {
		if line == "" {
			continue
		}
		var e TranscriptEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("transcript: %q: %v", line, err)
		}
		evs = append(evs, e)
	}
	return out.String(), evs
}

// The ordinary turn: input, answer, back to the user.
func TestRunner_APlainTurnPrintsTheAnswer(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("Paris"), done("stop")},
	}}
	out, evs := runWith(t, fg, NewRegistry(), "capital of France?\n")

	if !strings.Contains(out, "Paris") {
		t.Errorf("out = %q, want the answer", out)
	}
	if evs[len(evs)-1].State != "idle" {
		t.Errorf("final state = %q, want idle", evs[len(evs)-1].State)
	}
}

// A requested tool runs, and the turn resumes with its result.
//
// This is the end-to-end property the whole design exists for, asserted on
// the TRANSCRIPT rather than the prose: a tool was called, by name, with
// these arguments, and the turn ended cleanly.
func TestRunner_ARequestedToolRunsAndTheAnswerFollows(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("the answer is 42"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{toolCall("c1", "local__read_file", `{"path":"notes.md"}`), done("tool_calls")},
		{text("It says 42."), done("stop")},
	}}
	out, evs := runWith(t, fg, reg, "what do my notes say?\n")

	var calledTool, sawResult bool
	for _, e := range evs {
		if e.Kind == "tool_call" && e.Tool == "local__read_file" {
			calledTool = true
			var a map[string]string
			if err := json.Unmarshal(e.Args, &a); err != nil || a["path"] != "notes.md" {
				t.Errorf("tool args = %s", e.Args)
			}
		}
		if e.Kind == "tool_result" && e.Error == "" {
			sawResult = true
		}
	}
	if !calledTool || !sawResult {
		t.Fatalf("transcript did not record the call and its result: %+v", evs)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("out = %q, want the follow-up answer", out)
	}

	// The second request must carry the assistant's own call AND the result,
	// or the model is answering a question it cannot see it asked.
	if len(fg.seen) != 2 {
		t.Fatalf("sent %d turns, want 2", len(fg.seen))
	}
	var sawCalls, sawToolRole bool
	for _, m := range fg.seen[1].Messages {
		if len(m.ToolCalls) > 0 {
			sawCalls = true
		}
		if m.Role == "tool" && m.ToolCallID == "c1" {
			sawToolRole = true
		}
	}
	if !sawCalls {
		t.Error("the resumed turn omits the assistant's tool_calls")
	}
	if !sawToolRole {
		t.Error("the resumed turn omits the tool result, or loses its id")
	}
}

// Tool definitions reach the gateway, so the model can choose one.
func TestChatRequest_CarriesToolsAndToolResults(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(ReadFile(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{{text("ok"), done("stop")}}}
	runWith(t, fg, reg, "hello\n")

	if len(fg.seen) == 0 || fg.seen[0].Tools == nil {
		t.Fatal("no tools were sent; the model cannot choose one it is not offered")
	}
	var defs []struct {
		Type     string `json:"type"`
		Function struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(fg.seen[0].Tools, &defs); err != nil {
		t.Fatalf("tools are not the OpenAI shape: %v", err)
	}
	if len(defs) != 1 || defs[0].Function.Name != "local__read_file" {
		t.Fatalf("defs = %+v, want the qualified name", defs)
	}
	if len(defs[0].Function.Parameters) == 0 {
		t.Error("the schema is absent, so the model cannot form arguments")
	}
}

// An unknown tool is answered, not fatal.
//
// A hallucinated name must reach the model so its next turn can correct.
func TestRunner_AnUnknownToolIsAnsweredNotFatal(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{toolCall("c1", "local__delete_everything", `{}`), done("tool_calls")},
		{text("sorry, I cannot do that"), done("stop")},
	}}
	out, evs := runWith(t, fg, NewRegistry(), "delete my files\n")

	var failed bool
	for _, e := range evs {
		if e.Kind == "tool_result" && e.Error != "" {
			failed = true
		}
	}
	if !failed {
		t.Error("an unknown tool did not record a failure")
	}
	if !strings.Contains(out, "cannot") {
		t.Errorf("the model's correction did not reach the user: %q", out)
	}
}

// A stream that stops without a terminal event still ends the turn.
//
// Otherwise the loop waits forever for a Done that is not coming, and the
// shell hangs with no output and no error.
func TestRunner_AStreamWithNoTerminalEventStillEndsTheTurn(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("half an ans")}, // no done, no error
	}}
	out, evs := runWith(t, fg, NewRegistry(), "hello\n")

	if len(evs) == 0 || evs[len(evs)-1].State != "idle" {
		t.Fatalf("the turn did not end; the shell would hang. events: %+v", evs)
	}
	if !strings.Contains(out, "half an ans") {
		t.Errorf("the partial answer was discarded: %q", out)
	}
}

// Scripted and interactive take the same path.
//
// THE REQUIREMENT SET BEFORE A LINE WAS WRITTEN. The runner changes its
// ports, not its logic — so a shell-script test exercises the code a person
// exercises, rather than a parallel implementation that can drift.
func TestRunner_ScriptedAndInteractiveTakeTheSamePath(t *testing.T) {
	script := func() []TranscriptEvent {
		fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
			{text("one"), done("stop")},
			{text("two"), done("stop")},
		}}
		_, evs := runWith(t, fg, NewRegistry(), "first\nsecond\n")
		return evs
	}

	a, b := script(), script()
	if len(a) != len(b) {
		t.Fatalf("two identical scripted runs produced %d and %d events", len(a), len(b))
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].State != b[i].State {
			t.Fatalf("event %d differs between identical runs: %+v vs %+v", i, a[i], b[i])
		}
	}
	// Two inputs, so two turns and two returns to idle.
	var idles int
	for _, e := range a {
		if e.Kind == "await_input" {
			idles++
		}
	}
	if idles != 2 {
		t.Errorf("got %d await_input, want 2 — one per input", idles)
	}
}

// Reasoning is not printed into the answer stream.
func TestRunner_ReasoningDoesNotReachStdout(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{{
		{Kind: aigateway.StreamReasoning, Text: "the user wants the capital"},
		text("Paris"), done("stop"),
	}}}
	out, evs := runWith(t, fg, NewRegistry(), "capital?\n")

	if strings.Contains(out, "the user wants") {
		t.Errorf("reasoning leaked into the answer: %q", out)
	}
	var sawReasoning bool
	for _, e := range evs {
		if e.Kind == "reasoning" {
			sawReasoning = true
		}
	}
	if !sawReasoning {
		t.Error("reasoning was dropped entirely; a caller that wants it cannot get it")
	}
}

// A closed output stops the shell rather than spinning.
//
// If stdout has gone — a piped reader closed, a terminal killed —
// continuing means spending tokens on output nobody receives. A shell that
// keeps going in that state is one that runs up a bill silently.
func TestRunner_AClosedOutputStopsTheRun(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("one"), done("stop")},
		{text("two"), done("stop")},
	}}
	r := &Runner{Model: "claude", Client: fg, Tools: NewRegistry(),
		Out: brokenWriter{}, Transcript: nil}

	err := r.Run(context.Background(), NewPipeLines(strings.NewReader("a\nb\n")), "", 5)
	if err == nil {
		t.Fatal("a closed output produced no error; the shell would keep spending")
	}
	if len(fg.seen) > 1 {
		t.Errorf("sent %d turns after the output closed, want 1", len(fg.seen))
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// A turn the gateway refused makes the whole run fail.
//
// THE BUG THIS EXISTS FOR was found by the demo, not by a test: every turn
// of a scripted run was refused with an expired key, the refusal was
// printed, and the process exited 0. A harness reading the exit status saw
// a passing run in which nothing had happened. An error that only ever
// reaches stdout is invisible to everything downstream of stdout.
func TestRunner_AnErroredTurnFailsTheRun(t *testing.T) {
	fg := &fakeGateway{errs: []error{aigateway.ErrUnauthenticated}}
	var out, tr bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: NewRegistry(),
		Out: &out, Transcript: NewTranscript(&tr)}

	err := r.Run(context.Background(), NewPipeLines(strings.NewReader("hello\n")), "", 5)
	if err == nil {
		t.Fatal("run returned nil after every turn was refused")
	}
	if !errors.Is(err, aigateway.ErrUnauthenticated) {
		t.Errorf("err = %v, want it to carry the refusal so the caller can act on it", err)
	}
	// Still shown. Failing the run must not cost the user the message.
	if !strings.Contains(out.String(), aigateway.ErrUnauthenticated.Error()) {
		t.Errorf("out = %q, want the refusal printed as well as returned", out.String())
	}
}

// One failed turn does not abandon the rest of the script.
//
// Aborting on the first error under --script while an interactive session
// carried on would be the two paths DECIDING differently, which is the line
// scripted mode may not cross. So the run continues, and reports at the end.
func TestRunner_AnErroredTurnStillRunsTheRestOfTheScript(t *testing.T) {
	fg := &fakeGateway{
		errs:  []error{aigateway.ErrUnauthenticated, nil},
		turns: [][]aigateway.StreamChunk{{text("Paris"), done("stop")}},
	}
	var out, tr bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: NewRegistry(),
		Out: &out, Transcript: NewTranscript(&tr)}

	err := r.Run(context.Background(), NewPipeLines(strings.NewReader("first\nsecond\n")), "", 5)
	if err == nil {
		t.Fatal("run returned nil although a turn was refused")
	}
	if !strings.Contains(out.String(), "Paris") {
		t.Errorf("out = %q, want the second turn to have run anyway", out.String())
	}
	if !strings.Contains(err.Error(), "1 of this run's turns failed") {
		t.Errorf("err = %v, want it to count only the turn that failed", err)
	}
}

// A run in which nothing went wrong returns nil.
//
// The guard on the test above: an exit status that is always non-zero
// reports no more than one that is always zero.
func TestRunner_ACleanRunReturnsNil(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{{text("Paris"), done("stop")}}}
	var out, tr bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: NewRegistry(),
		Out: &out, Transcript: NewTranscript(&tr)}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("hello\n")), "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// Narration before a tool call does not run into the answer that follows.
//
// Measured on deepseek, which says "I'll read the file." before calling
// one. The two turns' text was printed back to back with nothing between
// them: "I'll read the file.Router cannot see...".
func TestRunner_NarrationBeforeAToolDoesNotRunIntoTheAnswer(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("I'll read the file."), toolCall("c1", "local__read_file", `{"path":"notes.md"}`), done("tool_calls")},
		{text("The answer."), done("stop")},
	}}
	out, _ := runWith(t, fg, reg, "what is in notes.md?\n")

	if strings.Contains(out, "file.The answer.") {
		t.Errorf("out = %q, want the two turns separated", out)
	}
	if !strings.Contains(out, "file.\nThe answer.") {
		t.Errorf("out = %q, want a single break between them", out)
	}
}

// Text that already ends in a newline gains no second one.
//
// The guard on the fix above: a blanket newline per turn would double-space
// every model that ends its narration properly.
func TestRunner_TextAlreadyEndingInANewlineGainsNoSecond(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("Reading.\n"), toolCall("c1", "local__read_file", `{"path":"notes.md"}`), done("tool_calls")},
		{text("The answer."), done("stop")},
	}}
	out, _ := runWith(t, fg, reg, "what is in notes.md?\n")

	if strings.Contains(out, "Reading.\n\nThe answer.") {
		t.Errorf("out = %q, want no blank line inserted", out)
	}
}

// Two tool calls in one turn both run, and the turn resumes ONCE with both
// results.
//
// Live parallel calls are currently broken upstream — the gateway stamps
// every streamed tool call with index 0 — so this asserts the property our
// side must hold when that is fixed, rather than waiting for the wire.
// Resuming after the first result would answer a question the model had
// not finished asking.
func TestRunner_TwoToolCallsInOneTurnBothRunBeforeItResumes(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{
			toolCall("call_1", "local__read_file", `{"path":"a.md"}`),
			toolCall("call_2", "local__read_file", `{"path":"b.md"}`),
			done("tool_calls"),
		},
		{text("Both read."), done("stop")},
	}}
	_, evs := runWith(t, fg, reg, "read both files\n")

	var ran int
	for _, e := range evs {
		if e.Kind == "tool_result" && e.Error == "" {
			ran++
		}
	}
	if ran != 2 {
		t.Errorf("%d tools ran, want 2 — a call was dropped", ran)
	}

	// Exactly two turns: the first ask, and one resume carrying BOTH
	// results. Three would mean the turn resumed after the first result.
	if len(fg.seen) != 2 {
		t.Fatalf("%d turns sent, want 2", len(fg.seen))
	}
	ids := map[string]bool{}
	for _, m := range fg.seen[1].Messages {
		if m.Role == "tool" {
			ids[m.ToolCallID] = true
		}
	}
	if !ids["call_1"] || !ids["call_2"] {
		t.Errorf("resumed turn carried tool ids %v, want both call_1 and call_2", ids)
	}
}

// Narration before a tool call is separated from the tool line.
//
// Seen live on glm: "I'll create it with a note line:· local__write_file".
// The earlier fix covered text before the next MODEL turn and missed the
// tool line, which is the boundary between what the model said and what
// the machine is about to do — the one place the two must not merge.
func TestRunner_NarrationBeforeAToolLineIsSeparated(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("I'll read it:"), toolCall("c1", "local__read_file", `{"path":"notes.md"}`), done("tool_calls")},
		{text("Done."), done("stop")},
	}}
	// A UI that marks where the tool line would go, so the test asserts
	// on the boundary rather than on the rendering.
	ui := &markerUI{out: &out}
	r := &Runner{Model: "glm", Client: fg, Tools: reg, Out: &out, UI: ui,
		Transcript: NewTranscript(&bytes.Buffer{})}
	if err := r.Run(context.Background(),
		NewPipeLines(strings.NewReader("read it\n")), "", 5); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(out.String(), "read it:<TOOL") {
		t.Errorf("the narration ran into the tool line:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "read it:\n<TOOL") {
		t.Errorf("out = %q, want a single break before the tool line", out.String())
	}
}

// markerUI writes a recognisable token where a tool line would appear.
type markerUI struct{ out *bytes.Buffer }

func (m *markerUI) TurnStarted() {}
func (m *markerUI) FirstOutput() {}
func (m *markerUI) ToolStarted(name string, _ json.RawMessage) {
	m.out.WriteString("<TOOL " + name + ">\n")
}
func (m *markerUI) ToolFinished(string, bool, int, string) {}
func (m *markerUI) ToolLinks([]string)                     {}
func (m *markerUI) Note(string, ...any)                    {}
func (m *markerUI) Highlight(string, ...any)               {}
func (m *markerUI) Progress(string, ...any)                {}

// With no one to ask, the budget stops the turn rather than hanging or
// helping itself to more.
func TestRunner_TheBudgetQuestionWithNobodyToAskIsANo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{toolCall("c1", "local__read_file", `{"path":"a.md"}`), done("tool_calls")},
		{toolCall("c2", "local__read_file", `{"path":"a.md"}`), done("tool_calls")},
	}}

	var out, tr bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: reg,
		Out: &out, Transcript: NewTranscript(&tr)}
	err := r.Run(context.Background(), NewPipeLines(strings.NewReader("go\n")), "", 1)

	if err == nil {
		t.Fatal("run succeeded; an unattended shell that hit its budget " +
			"reported nothing wrong and a script above it would carry on")
	}
	if len(fg.seen) != 2 {
		t.Errorf("sent %d turns, want 2 — the second tool must not have run", len(fg.seen))
	}
	if !strings.Contains(out.String(), "budget") {
		t.Errorf("out = %q, want the stop to name the budget", out.String())
	}
}

// With someone to ask, yes resumes the held work in the same input.
func TestRunner_AnsweringTheBudgetQuestionResumesTheWork(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{toolCall("c1", "local__read_file", `{"path":"a.md"}`), done("tool_calls")},
		{toolCall("c2", "local__read_file", `{"path":"a.md"}`), done("tool_calls")},
		{text("both read."), done("stop")},
	}}

	var out, tr bytes.Buffer
	var asked [][3]int
	r := &Runner{Model: "claude", Client: fg, Tools: reg,
		Out: &out, Transcript: NewTranscript(&tr),
		AskContinue: func(pending, used, budget int) int {
			asked = append(asked, [3]int{pending, used, budget})
			return pending
		}}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("go\n")), "", 1); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(asked) != 1 {
		t.Fatalf("asked %d times, want once", len(asked))
	}
	if asked[0] != [3]int{1, 1, 1} {
		t.Errorf("asked with %v, want pending/used/budget of 1/1/1", asked[0])
	}
	if len(fg.seen) != 3 {
		t.Fatalf("sent %d turns, want 3 — a yes that did not resume makes the "+
			"user re-ask and re-run everything already established", len(fg.seen))
	}
	if !strings.Contains(out.String(), "both read.") {
		t.Errorf("out = %q, want the answer after the continuation", out.String())
	}
}

// A pull request the model opened must reach the operator.
//
// Tool output is summarised as a character count, so before this the
// URL existed only in the model's context: it could open a PR, say so
// in prose or not, and the operator had nothing to click.
func TestRunner_APullRequestInToolOutputReachesTheScreen(t *testing.T) {
	root := t.TempDir()
	const url = "https://github.com/mikelear/leartech-ba-service/pull/76"
	if err := os.WriteFile(filepath.Join(root, "out.txt"),
		[]byte("created "+url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{toolCall("c1", "local__read_file", `{"path":"out.txt"}`), done("tool_calls")},
		{text("opened it."), done("stop")},
	}}
	ui := &linkUI{}
	var out, tr bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: reg, UI: ui,
		Out: &out, Transcript: NewTranscript(&tr)}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("go\n")), "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(ui.links) != 1 || ui.links[0] != url {
		t.Errorf("links = %v, want the pull request — the result was "+
			"summarised as a character count, so without this the operator "+
			"has nothing to open", ui.links)
	}
}

type linkUI struct {
	Quiet
	links []string
}

func (l *linkUI) ToolLinks(urls []string) { l.links = append(l.links, urls...) }

// A large grant carries through the rest of the turn.
//
// INTEGRATION, not a unit test of the grant: the number has to survive
// the approver, the event, the loop's accounting and every following
// turn. A grant honoured once and forgotten would ask again two calls
// later, which is the interruption it exists to remove.
func TestRunner_ALargeGrantCarriesThroughTheWholeTurn(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}

	// Ten turns, each wanting one tool, against a budget of one.
	var turns [][]aigateway.StreamChunk
	for i := 0; i < 10; i++ {
		turns = append(turns, []aigateway.StreamChunk{
			toolCall(fmt.Sprintf("c%d", i), "local__read_file", `{"path":"a.md"}`),
			done("tool_calls"),
		})
	}
	turns = append(turns, []aigateway.StreamChunk{text("done."), done("stop")})
	fg := &fakeGateway{turns: turns}

	asked := 0
	var out, tr bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: reg,
		Out: &out, Transcript: NewTranscript(&tr),
		AskContinue: func(_, _, _ int) int { asked++; return 50 }}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("go\n")), "", 1); err != nil {
		t.Fatalf("run: %v", err)
	}

	if asked != 1 {
		t.Errorf("asked %d times for a grant of 50 across 10 calls; the "+
			"number was not carried", asked)
	}
	if len(fg.seen) != 11 {
		t.Errorf("sent %d turns, want 11 — the work did not run to completion",
			len(fg.seen))
	}
}

// A declined grant still stops, however large the number offered.
func TestRunner_ARefusalStopsRegardlessOfTheNumber(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.Add(ReadFile(root)); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{toolCall("c1", "local__read_file", `{"path":"a.md"}`), done("tool_calls")},
		{toolCall("c2", "local__read_file", `{"path":"a.md"}`), done("tool_calls")},
	}}
	var out, tr bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: reg,
		Out: &out, Transcript: NewTranscript(&tr),
		AskContinue: func(_, _, _ int) int { return 0 }}

	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("go\n")), "", 1); err == nil {
		t.Fatal("a refused budget did not fail the run")
	}
	if len(fg.seen) != 2 {
		t.Errorf("sent %d turns, want 2 — the second tool must not have run",
			len(fg.seen))
	}
}

// EVERY TOOL RESULT RE-SENDS THE WHOLE CONVERSATION. A session with forty
// tool calls re-sends every earlier tool result forty times, and here they
// carry file contents — which is how an ordinary exploration exhausted a
// monthly budget on 2026-09-25.
//
// The first attempt marked the SYSTEM message and was measured on
// 2026-09-26 to mark nothing at all: the built-in prompt is 765 characters
// against a ~1024-TOKEN floor, and the cluster serves no addition by
// default. The tail is both reachable and the part that grows.
func TestToWire_MarksTheEndOfTheConversation(t *testing.T) {
	got := toWire([]Message{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "tool", ToolCallID: "t1", Content: "file contents"},
	})
	if len(got) != 4 {
		t.Fatalf("got %d messages", len(got))
	}
	last := got[3]
	if len(last.Blocks) != 1 || last.Blocks[0].CacheControl == nil {
		t.Fatalf("the end of the conversation is not marked cacheable: %+v", last)
	}
	if last.Blocks[0].Text != "file contents" {
		t.Errorf("the block does not carry the message text: %q", last.Blocks[0].Text)
	}
	// Blocks and Content are alternatives. Leaving both set sends the text
	// twice — paying double for the thing being cached.
	if last.Content != "" {
		t.Error("Content was left set alongside Blocks, so the text is sent twice")
	}
	// The system message must NOT be separately marked: it is inside the
	// cached prefix already, and a second breakpoint spends one of four.
	if len(got[0].Blocks) != 0 {
		t.Errorf("the system message was marked as well: %+v", got[0])
	}
}

// NO SIZE GATE. The old gate held a per-provider floor in the client and
// was wrong for Haiku (2048), meaningless for z.ai and Ollama, and never
// satisfied by the real prompt. A short prefix is simply not cached, and
// the reply says so — measurement beats a hardcoded prediction.
func TestToWire_MarksTheTailEvenWhenItIsShort(t *testing.T) {
	got := toWire([]Message{{Role: "user", Content: "hi"}})
	if len(got[0].Blocks) != 1 || got[0].Blocks[0].CacheControl == nil {
		t.Errorf("a short tail was left unmarked, reintroducing the size gate: %+v", got[0])
	}
}

// EXACTLY ONE breakpoint. Anthropic permits four; spending one per message
// would exhaust them on a four-message conversation and fail thereafter.
func TestToWire_MarksExactlyOneMessage(t *testing.T) {
	got := toWire([]Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "u"},
		{Role: "assistant", Content: "a"},
		{Role: "tool", ToolCallID: "t1", Content: "t"},
	})
	marked := 0
	for _, m := range got {
		for _, b := range m.Blocks {
			if b.CacheControl != nil {
				marked++
			}
		}
	}
	if marked != 1 {
		t.Errorf("%d messages carry a breakpoint, want exactly 1", marked)
	}
}

// AN ASSISTANT TURN THAT ONLY CALLED A TOOL HAS NO TEXT. Marking it would
// put the breakpoint on an empty string, caching LESS than the previous
// turn did, so the search walks back to the last real text.
func TestToWire_SkipsAMessageWithNoTextToMark(t *testing.T) {
	got := toWire([]Message{
		{Role: "user", Content: "read it"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "read_file"}}},
	})
	if len(got) != 2 {
		t.Fatalf("got %d messages", len(got))
	}
	if len(got[1].Blocks) != 0 {
		t.Errorf("a message with no text was marked: %+v", got[1])
	}
	if len(got[0].Blocks) != 1 || got[0].Blocks[0].CacheControl == nil {
		t.Errorf("the breakpoint did not fall back to the last message with text: %+v", got[0])
	}
	// The tool call itself must survive the fallback.
	if got[1].ToolCalls == nil {
		t.Error("the tool call was lost")
	}
}

// Nothing to send is not an error and must not mark a message that is not
// there.
func TestToWire_MarksNothingWhenThereIsNothingToSend(t *testing.T) {
	if got := toWire(nil); len(got) != 0 {
		t.Errorf("toWire(nil) = %+v, want empty", got)
	}
	got := toWire([]Message{{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "x"}}}})
	if len(got) != 1 {
		t.Fatalf("got %d messages", len(got))
	}
	if len(got[0].Blocks) != 0 {
		t.Errorf("marked a message with no text when no other was available: %+v", got[0])
	}
}

func usageChunk(prompt, completion, cachedRead int) aigateway.StreamChunk {
	return aigateway.StreamChunk{Kind: aigateway.StreamUsage, Usage: &aigateway.ChatUsage{
		PromptTokens: prompt, CompletionTokens: completion,
		TotalTokens:         prompt + completion,
		PromptTokensDetails: &aigateway.PromptTokensDetails{CachedTokens: cachedRead},
		LeartechCache:       &aigateway.LeartechCache{},
	}}
}

// THE SHELL COULD NOT SEE WHAT A TURN COST. It streams, the stream carries
// usage only on a trailing frame, and nothing consumed it — so the session
// that exhausted a budget on 2026-09-25 reported nothing on the way there
// while the one-shot `chat` path printed token counts for every call.
func TestRunner_ReportsWhatTheTurnCost(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("hello"), done("stop"), usageChunk(4300, 12, 4096)},
	}}
	var got []aigateway.ChatUsage
	var out bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: NewRegistry(), Out: &out,
		OnUsage: func(u aigateway.ChatUsage) { got = append(got, u) }}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("hi\n")), "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("OnUsage called %d times, want once per turn", len(got))
	}
	if got[0].PromptTokens != 4300 {
		t.Errorf("prompt tokens = %d, want 4300", got[0].PromptTokens)
	}
	if got[0].CachedTokens() != 4096 {
		t.Errorf("cached tokens = %d, want 4096", got[0].CachedTokens())
	}
	// The usage frame must not have leaked into the answer.
	if strings.Contains(out.String(), "4300") {
		t.Errorf("usage was printed into the answer stream: %q", out.String())
	}
}

// A usage frame arrives AFTER the finish reason, and must contribute
// NOTHING to the conversation or the output.
//
// Asserted by DIFFERENCE against the same turn without the frame, because
// an assertion on the text alone passes whatever the frame injects — the
// first version of this test did exactly that and survived a deliberate
// leak of a ModelText event.
func TestRunner_UsageAfterTheFinishReasonDoesNotExtendTheTurn(t *testing.T) {
	run := func(chunks []aigateway.StreamChunk) (string, int) {
		fg := &fakeGateway{turns: [][]aigateway.StreamChunk{chunks}}
		var out bytes.Buffer
		r := &Runner{Model: "claude", Client: fg, Tools: NewRegistry(), Out: &out}
		if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("q\n")), "", 5); err != nil {
			t.Fatalf("run: %v", err)
		}
		return out.String(), len(fg.seen)
	}

	without, turnsWithout := run([]aigateway.StreamChunk{text("answer"), done("stop")})
	with, turnsWith := run([]aigateway.StreamChunk{text("answer"), done("stop"), usageChunk(10, 2, 0)})

	if with != without {
		t.Errorf("the usage frame changed the output:\n with:    %q\n without: %q", with, without)
	}
	if turnsWith != turnsWithout {
		t.Errorf("the usage frame changed the turn count: %d vs %d", turnsWith, turnsWithout)
	}
	if turnsWith != 1 {
		t.Errorf("the gateway was called %d times, want 1", turnsWith)
	}
}

// A supplier that reports no usage at all — Ollama serves cache_control and
// reports nothing — must not look like a failure or a missing turn.
func TestRunner_ASupplierThatReportsNoUsageIsNotAnError(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("hi"), done("stop")},
	}}
	var out bytes.Buffer
	called := false
	r := &Runner{Model: "qwen", Client: fg, Tools: NewRegistry(), Out: &out,
		OnUsage: func(aigateway.ChatUsage) { called = true }}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("hi\n")), "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}
	if called {
		t.Error("OnUsage fired for a supplier that reported nothing, so absent reads as zero")
	}
}

// A nil OnUsage is the scripted case and must not panic.
func TestRunner_UsageWithNobodyWatchingIsFine(t *testing.T) {
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{text("hi"), done("stop"), usageChunk(5, 1, 0)},
	}}
	var out bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: NewRegistry(), Out: &out}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("hi\n")), "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// THE BREAKPOINT MOVES WITH THE CONVERSATION. A second turn must mark its
// own tail, not the one the first turn marked — otherwise the prefix stops
// growing and every later turn re-bills the accumulated tool results.
func TestRunner_EachTurnMarksItsOwnTail(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(ListDir(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGateway{turns: [][]aigateway.StreamChunk{
		{toolCall("t1", "list_dir", `{"path":"."}`), done("tool_calls")},
		{text("done"), done("stop")},
	}}
	var out bytes.Buffer
	r := &Runner{Model: "claude", Client: fg, Tools: reg, Out: &out}
	if err := r.Run(context.Background(), NewPipeLines(strings.NewReader("look\n")), "", 5); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fg.seen) != 2 {
		t.Fatalf("saw %d requests, want 2", len(fg.seen))
	}
	for n, req := range fg.seen {
		marked := -1
		for i, m := range req.Messages {
			for _, b := range m.Blocks {
				if b.CacheControl != nil {
					marked = i
				}
			}
		}
		if marked == -1 {
			t.Fatalf("turn %d marked no breakpoint", n+1)
		}
		if want := len(req.Messages) - 1; marked != want {
			t.Errorf("turn %d marked message %d of %d; the breakpoint did not move to the tail",
				n+1, marked, len(req.Messages))
		}
	}
	// The second turn must be strictly longer, or there is no growing
	// prefix to cache in the first place.
	if len(fg.seen[1].Messages) <= len(fg.seen[0].Messages) {
		t.Errorf("turn 2 sent %d messages, turn 1 sent %d — the history is not accumulating",
			len(fg.seen[1].Messages), len(fg.seen[0].Messages))
	}
}

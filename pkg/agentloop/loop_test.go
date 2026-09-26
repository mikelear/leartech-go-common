package agentloop

import (
	"encoding/json"
	"errors"
	"testing"
)

func kinds(as []Action) []ActionKind {
	out := make([]ActionKind, 0, len(as))
	for _, a := range as {
		out = append(out, a.Kind)
	}
	return out
}

func call(id, name string) *ToolCall {
	return &ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{}`)}
}

// An idle loop asks for input and sends nothing.
func TestLoop_AsksForInputWhenIdle(t *testing.T) {
	l := New("be helpful", 5)
	if l.State() != Idle {
		t.Fatalf("state = %v, want idle", l.State())
	}
	// A stray tool result with no turn in flight must not resume anything.
	if got := l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "x")}); got != nil {
		t.Errorf("a tool result arrived with no turn in flight and produced %v", kinds(got))
	}
}

// The ordinary round trip: input, text, done, back to the user.
func TestLoop_APlainTurnEndsAtTheUser(t *testing.T) {
	l := New("", 5)

	got := l.Step(Event{Kind: UserInput, Text: "hello"})
	if len(got) != 1 || got[0].Kind != SendTurn {
		t.Fatalf("got %v, want one SendTurn", kinds(got))
	}
	if n := len(got[0].Send); n != 1 {
		t.Errorf("sent %d messages, want 1 (the user's)", n)
	}

	l.Step(Event{Kind: ModelText, Text: "hi"})
	got = l.Step(Event{Kind: ModelDone, Finish: "stop"})
	if len(got) != 1 || got[0].Kind != AwaitInput {
		t.Fatalf("got %v, want AwaitInput", kinds(got))
	}
	if l.State() != Idle {
		t.Errorf("state = %v, want idle", l.State())
	}
}

// A tool call runs, and the turn resumes with its result.
func TestLoop_AToolCallIsRunThenTheTurnResumes(t *testing.T) {
	l := New("", 5)
	l.Step(Event{Kind: UserInput, Text: "read the file"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "local__read_file")})

	got := l.Step(Event{Kind: ModelDone, Finish: "tool_calls"})
	if len(got) != 1 || got[0].Kind != RunTool {
		t.Fatalf("got %v, want one RunTool", kinds(got))
	}
	if l.State() != Running {
		t.Errorf("state = %v, want running", l.State())
	}

	got = l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "local__read_file"),
		Result: "file contents", ResultOK: true})
	if len(got) != 1 || got[0].Kind != SendTurn {
		t.Fatalf("got %v, want SendTurn", kinds(got))
	}
	// The model must see that it ASKED, not only what came back.
	var sawAssistantCall, sawToolResult bool
	for _, m := range got[0].Send {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 {
			sawAssistantCall = true
		}
		if m.Role == "tool" && m.Content == "file contents" {
			sawToolResult = true
		}
	}
	if !sawAssistantCall {
		t.Error("the resumed turn omits the assistant's own tool call; the model " +
			"cannot tell what it asked for")
	}
	if !sawToolResult {
		t.Error("the resumed turn omits the tool result")
	}
}

// Several calls in one turn all answer before the turn resumes.
//
// Resuming after the first would answer a question the model has not
// finished asking, and everything after is built on a partial reply.
func TestLoop_SeveralToolCallsAllAnswerBeforeTheTurnResumes(t *testing.T) {
	l := New("", 5)
	l.Step(Event{Kind: UserInput, Text: "read two files"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "local__read_file")})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "local__read_file")})

	got := l.Step(Event{Kind: ModelDone, Finish: "tool_calls"})
	if len(got) != 2 {
		t.Fatalf("got %v, want two RunTool", kinds(got))
	}

	if got := l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "x"), Result: "one"}); got != nil {
		t.Fatalf("the turn resumed after ONE of two results: %v", kinds(got))
	}
	got = l.Step(Event{Kind: ToolResult, ToolCall: call("c2", "x"), Result: "two"})
	if len(got) != 1 || got[0].Kind != SendTurn {
		t.Fatalf("got %v, want SendTurn once both returned", kinds(got))
	}
}

// The tool budget stops a loop that will not stop itself.
//
// THE FAILURE MODE OF THIS WHOLE DESIGN. A model can call a tool, read the
// result and call again forever; each round costs money and runs something
// on the user's machine. The bound is per user input, so a long
// conversation is not penalised for being long.
func TestLoop_StopsAtTheToolBudget(t *testing.T) {
	l := New("", 2)
	l.Step(Event{Kind: UserInput, Text: "go"})

	// Two calls are within budget.
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "t")})
	if got := l.Step(Event{Kind: ModelDone}); len(got) != 2 {
		t.Fatalf("got %v, want two RunTool within budget", kinds(got))
	}
	l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c2", "t")})

	// A third exceeds it and must stop rather than run.
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c3", "t")})
	got := l.Step(Event{Kind: ModelDone})
	for _, a := range got {
		if a.Kind == RunTool {
			t.Fatal("a tool ran past the budget; nothing bounds the loop")
		}
	}
	if len(got) != 1 || got[0].Kind != AskToContinue {
		t.Fatalf("got %v, want the budget put to the operator", kinds(got))
	}
	if got[0].Pending != 1 || got[0].Used != 2 || got[0].Budget != 2 {
		t.Errorf("asked with pending=%d used=%d budget=%d, want 1/2/2 so the "+
			"question says how much more out of what",
			got[0].Pending, got[0].Used, got[0].Budget)
	}
	if l.State() != Asking {
		t.Errorf("state = %v, want asking while the question is outstanding", l.State())
	}
}

// Answering yes runs the very calls the budget interrupted, rather than
// making the user re-ask and re-establish everything.
func TestLoop_TheBudgetAsksAndAContinueRunsThePendingCalls(t *testing.T) {
	l := New("", 1)
	l.Step(Event{Kind: UserInput, Text: "go"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")})

	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "second")})
	if got := l.Step(Event{Kind: ModelDone}); len(got) != 1 || got[0].Kind != AskToContinue {
		t.Fatalf("got %v, want the question", kinds(got))
	}

	got := l.Step(Event{Kind: BudgetAnswer, Allow: true})
	if len(got) != 1 || got[0].Kind != RunTool {
		t.Fatalf("got %v, want the held call to run on yes", kinds(got))
	}
	if got[0].Call.ID != "c2" {
		t.Errorf("ran %q, want the pending call c2 — a continue that dropped "+
			"the work would make the user re-ask", got[0].Call.ID)
	}
	if l.State() != Running {
		t.Errorf("state = %v, want running", l.State())
	}
}

// Saying no ends the turn exactly as the unconditional stop used to.
func TestLoop_DecliningTheBudgetStopsAsItAlwaysDid(t *testing.T) {
	l := New("", 1)
	l.Step(Event{Kind: UserInput, Text: "go"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "t")})
	l.Step(Event{Kind: ModelDone})

	got := l.Step(Event{Kind: BudgetAnswer, Allow: false})
	for _, a := range got {
		if a.Kind == RunTool {
			t.Fatal("a tool ran after the operator said no")
		}
	}
	if len(got) != 2 || got[0].Kind != ShowError || got[1].Kind != AwaitInput {
		t.Fatalf("got %v, want the stop explained then input awaited", kinds(got))
	}
	if l.State() != Idle {
		t.Errorf("state = %v, want idle", l.State())
	}
}

// The held calls survive the question itself.
func TestLoop_AskingKeepsThePendingCalls(t *testing.T) {
	l := New("", 1)
	l.Step(Event{Kind: UserInput, Text: "go"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "a")})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c3", "b")})
	l.Step(Event{Kind: ModelDone})

	if len(l.pending) != 2 {
		t.Fatalf("pending = %d while asking, want both calls held", len(l.pending))
	}
	got := l.Step(Event{Kind: BudgetAnswer, Allow: true})
	if len(got) != 2 {
		t.Fatalf("got %v, want both held calls to run", kinds(got))
	}
}

// A yes for a question nobody asked must not raise the ceiling.
func TestLoop_ABudgetAnswerNobodyAskedForIsIgnored(t *testing.T) {
	l := New("", 1)
	l.Step(Event{Kind: UserInput, Text: "go"})

	if got := l.Step(Event{Kind: BudgetAnswer, Allow: true}); got != nil {
		t.Fatalf("got %v, want nothing from an unsolicited answer", kinds(got))
	}
	if l.maxTools != 1 {
		t.Errorf("maxTools = %d, want 1 — a stray yes raised the budget", l.maxTools)
	}
}

// Continuing grants one more budget, so the question comes round again.
func TestLoop_ContinuingGrantsOneMoreBudgetRatherThanRemovingIt(t *testing.T) {
	l := New("", 1)
	l.Step(Event{Kind: UserInput, Text: "go"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "t")})
	l.Step(Event{Kind: ModelDone})
	l.Step(Event{Kind: BudgetAnswer, Allow: true})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c2", "t")})

	// A third call is past the raised ceiling too, and must ask again.
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c3", "t")})
	got := l.Step(Event{Kind: ModelDone})
	if len(got) != 1 || got[0].Kind != AskToContinue {
		t.Fatalf("got %v, want a second question — one yes removed the ceiling "+
			"and the runaway the budget exists to catch is now unbounded",
			kinds(got))
	}
}

// A fresh user input resets the budget.
func TestLoop_TheBudgetIsPerInputNotPerSession(t *testing.T) {
	l := New("", 1)
	l.Step(Event{Kind: UserInput, Text: "one"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone}) // no more calls; back to idle

	l.Step(Event{Kind: UserInput, Text: "two"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "t")})
	got := l.Step(Event{Kind: ModelDone})
	if len(got) != 1 || got[0].Kind != RunTool {
		t.Fatalf("got %v: the budget did not reset on new input", kinds(got))
	}
}

// An interrupt returns to idle and sends nothing.
//
// A turn still sent after Ctrl-C spends money the user just asked not to
// spend.
func TestLoop_AnInterruptReturnsToIdleWithoutSending(t *testing.T) {
	l := New("", 5)
	l.Step(Event{Kind: UserInput, Text: "go"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone})

	got := l.Step(Event{Kind: Interrupt})
	for _, a := range got {
		if a.Kind == SendTurn || a.Kind == RunTool {
			t.Fatalf("an interrupt produced %v", a.Kind)
		}
	}
	if l.State() != Idle {
		t.Errorf("state = %v, want idle", l.State())
	}

	// And a late result from the abandoned tool must not resume it.
	if got := l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")}); got != nil {
		t.Errorf("a result from an interrupted turn resumed it: %v", kinds(got))
	}
}

// Reasoning stays separate from the answer.
func TestLoop_ReasoningIsNotShownAsAnswerText(t *testing.T) {
	l := New("", 5)
	l.Step(Event{Kind: UserInput, Text: "go"})

	got := l.Step(Event{Kind: ModelReasoning, Text: "thinking"})
	if len(got) != 1 || got[0].Kind != ShowReasoning {
		t.Fatalf("got %v, want ShowReasoning: merging it into the answer destroys "+
			"the distinction for everything downstream", kinds(got))
	}
}

// A stream error ends the turn and hands back, rather than hanging.
func TestLoop_AStreamErrorHandsBackToTheUser(t *testing.T) {
	l := New("", 5)
	l.Step(Event{Kind: UserInput, Text: "go"})

	got := l.Step(Event{Kind: ModelError, Err: errors.New("upstream said no")})
	if len(got) != 2 || got[0].Kind != ShowError || got[1].Kind != AwaitInput {
		t.Fatalf("got %v, want the error shown then input awaited", kinds(got))
	}
	if l.State() != Idle {
		t.Errorf("state = %v, want idle", l.State())
	}
}

// Input arriving mid-turn is dropped, not queued.
func TestLoop_InputDuringATurnIsNotActedOn(t *testing.T) {
	l := New("", 5)
	l.Step(Event{Kind: UserInput, Text: "first"})

	got := l.Step(Event{Kind: UserInput, Text: "second"})
	if got != nil {
		t.Fatalf("a second input mid-turn produced %v; queuing it would act on a "+
			"line the user can no longer see the context for", kinds(got))
	}
}

// The conversation handed out cannot be edited into the loop's history.
func TestLoop_TheSentConversationIsACopy(t *testing.T) {
	l := New("sys", 5)
	got := l.Step(Event{Kind: UserInput, Text: "hello"})
	got[0].Send[0].Content = "tampered"

	next := l.Step(Event{Kind: ModelDone})
	_ = next
	l.Step(Event{Kind: UserInput, Text: "again"})
	again := l.Step(Event{Kind: ModelDone})
	_ = again

	for _, m := range l.conversation() {
		if m.Content == "tampered" {
			t.Fatal("editing a handed-out Action mutated the loop's history")
		}
	}
}

// A grant of its own size is honoured, not rounded to one budget.
func TestLoop_AGrantOfItsOwnSizeIsHonoured(t *testing.T) {
	l := New("", 1)
	l.Step(Event{Kind: UserInput, Text: "go"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "t")})
	l.Step(Event{Kind: ModelDone})

	l.Step(Event{Kind: BudgetAnswer, Allow: true, Grant: 50})
	if l.maxTools != 51 {
		t.Fatalf("maxTools = %d, want 51 — a grant of 50 that became one "+
			"budget asks again in two calls, which is the interruption the "+
			"number exists to avoid", l.maxTools)
	}
	l.Step(Event{Kind: ToolResult, ToolCall: call("c2", "t")})

	// Twenty more calls must proceed without another question.
	for i := 0; i < 20; i++ {
		l.Step(Event{Kind: ModelToolCall, ToolCall: call("x", "t")})
		got := l.Step(Event{Kind: ModelDone})
		if len(got) != 1 || got[0].Kind != RunTool {
			t.Fatalf("call %d asked again: %v", i, kinds(got))
		}
		l.Step(Event{Kind: ToolResult, ToolCall: call("x", "t")})
	}
}

// A yes with no number keeps meaning what it always meant.
func TestLoop_AZeroGrantFallsBackToOneBudget(t *testing.T) {
	l := New("", 3)
	l.Step(Event{Kind: UserInput, Text: "go"})
	for i := 0; i < 4; i++ {
		l.Step(Event{Kind: ModelToolCall, ToolCall: call("c", "t")})
	}
	l.Step(Event{Kind: ModelDone})

	l.Step(Event{Kind: BudgetAnswer, Allow: true})
	if l.maxTools != 6 {
		t.Errorf("maxTools = %d, want 6 — a bare yes grants one more budget",
			l.maxTools)
	}
}

// A grant on a refusal changes nothing.
func TestLoop_AGrantIsIgnoredWhenTheAnswerIsNo(t *testing.T) {
	l := New("", 1)
	l.Step(Event{Kind: UserInput, Text: "go"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelDone})
	l.Step(Event{Kind: ToolResult, ToolCall: call("c1", "t")})
	l.Step(Event{Kind: ModelToolCall, ToolCall: call("c2", "t")})
	l.Step(Event{Kind: ModelDone})

	l.Step(Event{Kind: BudgetAnswer, Allow: false, Grant: 500})
	if l.maxTools != 1 {
		t.Errorf("maxTools = %d, want 1 — a refusal carrying a number must "+
			"not raise the ceiling", l.maxTools)
	}
}

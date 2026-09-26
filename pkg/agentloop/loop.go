package agentloop

import (
	"encoding/json"
	"fmt"
)

// Event is something that happened; the loop is told rather than fetching.
type Event struct {
	Kind EventKind

	// Text is the user's line (UserInput) or a delta (ModelText/ModelReasoning).
	Text string

	// ToolCall is a complete call the model asked for.
	ToolCall *ToolCall

	// Result is what a tool returned, and whether it failed.
	Result   string
	ResultOK bool

	// Finish is why the turn ended; Err is why it broke.
	Finish string
	Err    error

	// Allow is the operator's reply, when Kind is BudgetAnswer.
	Allow bool

	// Grant is how many more tool calls to permit, when Allow is true.
	//
	// ZERO MEANS THE DEFAULT, so a plain yes keeps its old meaning. // proven-by: TestLoop_AZeroGrantFallsBackToOneBudget
	// A large ask wants a large number: being asked every few calls
	// through a long investigation is the interruption the question
	// replaced, and someone who knows the work is big can say so once.
	//
	// proven-by: TestLoop_AGrantOfItsOwnSizeIsHonoured
	// proven-by: TestLoop_AZeroGrantFallsBackToOneBudget
	Grant int
}

// EventKind discriminates, rather than the loop inferring from which fields
// are set. Inferring shape from non-nil fields is the defect the gateway
// removed from its own stream in ai-gateway#81.
type EventKind int

// The events a loop is told about.
const (
	UserInput EventKind = iota
	ModelText
	ModelReasoning
	ModelToolCall
	ModelDone
	ModelError
	ToolResult
	Interrupt

	// BudgetAnswer is the operator's reply to AskToContinue.
	//
	// AN EVENT LIKE ANY OTHER, so the loop stays a pure state machine:
	// it asks by emitting an action and learns the answer by being told,
	// the same way it learns a tool returned. Reaching out for the
	// answer would put I/O in the one place that has none.
	//
	// proven-by: TestLoop_TheBudgetAsksAndAContinueRunsThePendingCalls
	BudgetAnswer
)

// Action is one instruction for the caller.
type Action struct {
	Kind ActionKind

	// Send is the conversation to send, when Kind is SendTurn.
	Send []Message

	// Call is the tool to run, when Kind is RunTool.
	Call *ToolCall

	// Text is what to show, when Kind is ShowText or ShowReasoning.
	Text string

	// Err is what went wrong, when Kind is ShowError.
	Err error

	// Pending, Used and Budget describe the tool budget, when Kind is
	// AskToContinue. The caller needs all three to ask a question worth
	// answering: how many more, how many already, and out of what.
	Pending int
	Used    int
	Budget  int
}

// ActionKind discriminates for the same reason EventKind does.
type ActionKind int

// Action kinds.
const (
	SendTurn ActionKind = iota
	RunTool
	ShowText
	ShowReasoning
	ShowError
	AwaitInput

	// AskToContinue reports that this input has reached its tool budget
	// and asks whether to allow another budget's worth.
	//
	// A QUESTION RATHER THAN AN ERROR. Stopping mid-investigation threw
	// away the work so far and made the operator re-ask, which re-ran
	// everything the model had already established. The pending calls
	// are kept while the question is outstanding, so answering yes
	// resumes rather than restarts.
	//
	// proven-by: TestLoop_TheBudgetAsksAndAContinueRunsThePendingCalls
	// proven-by: TestLoop_DecliningTheBudgetStopsAsItAlwaysDid
	AskToContinue
)

// ToolCall is one complete call. Arguments stay raw: the envelope is ours,
// the payload is the model's.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Message is one turn in the conversation the loop accumulates.
type Message struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

// State is where the loop is. Exported so a test can assert on it without
// reading actions, and so a transcript can name it.
type State int

// Where the loop can be.
const (
	Idle     State = iota // waiting for the user
	Awaiting              // a turn is in flight
	Running               // a tool is running
	Asking                // the budget question is outstanding
)

func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case Awaiting:
		return "awaiting"
	case Asking:
		return "asking"
	case Running:
		return "running"
	}
	return "unknown"
}

// Loop is the conversation and where it has got to.
//
// A PURE STATE MACHINE. Events go in, Actions come out, and nothing in
// this file touches a terminal, a network or a clock — which is what makes
// the loop driveable from a script, a requirement set before a line was
// written rather than discovered afterwards.
type Loop struct {
	state    State
	messages []Message

	// pending are tool calls the model asked for and that have not returned.
	//
	// A SLICE BECAUSE A MODEL MAY ASK FOR SEVERAL IN ONE TURN. Sending the
	// conversation back after the first result would answer a question the
	// model has not finished asking, and the turn that follows would be
	// built on a partial reply.
	pending []ToolCall

	// answered collects results until every pending call has one.
	answered []Message

	// maxTools bounds tool calls per user input.
	//
	// proven-by: TestLoop_StopsAtTheToolBudget
	// proven-by: TestLoop_TheBudgetIsPerInputNotPerSession
	//
	// A model can ask for a tool, read the result and ask again forever;
	// each round costs money and runs something on the user's machine.
	maxTools int
	used     int

	// budget is the original ceiling, kept so a granted continuation is
	// one more of what the operator asked for rather than one more of
	// whatever it has grown to.
	//
	// proven-by: TestLoop_ContinuingGrantsOneMoreBudgetRatherThanRemovingIt
	budget int
}

// New returns a loop waiting for input.
//
// proven-by: TestLoop_AsksForInputWhenIdle
func New(system string, maxTools int) *Loop {
	l := &Loop{state: Idle, maxTools: maxTools, budget: maxTools}
	if system != "" {
		l.messages = append(l.messages, Message{Role: "system", Content: system})
	}
	return l
}

// State reports where the loop is.
func (l *Loop) State() State { return l.state }

// Step advances the loop by one event.
//
// THE ONLY ENTRY POINT, so that a scripted run and an interactive run take
// the same path — a second way in would be a second place for the state to
// change.
//
// proven-by: TestLoop_AToolCallIsRunThenTheTurnResumes
// proven-by: TestLoop_SeveralToolCallsAllAnswerBeforeTheTurnResumes
// proven-by: TestLoop_StopsAtTheToolBudget
// proven-by: TestLoop_AnInterruptReturnsToIdleWithoutSending
func (l *Loop) Step(e Event) []Action {
	switch e.Kind {
	case UserInput:
		if l.state != Idle {
			// proven-by: TestLoop_InputDuringATurnIsNotActedOn
			return nil
		}
		l.used = 0
		l.messages = append(l.messages, Message{Role: "user", Content: e.Text})
		l.state = Awaiting
		return []Action{{Kind: SendTurn, Send: l.conversation()}}

	case ModelText:
		return []Action{{Kind: ShowText, Text: e.Text}}

	case ModelReasoning:
		// Separate from ShowText so a caller can dim it, collapse it, or
		// drop it. Merging them destroys that choice for everyone
		// downstream, which is the one irreversible decision in the stream.
		return []Action{{Kind: ShowReasoning, Text: e.Text}}

	case ModelToolCall:
		if e.ToolCall == nil {
			return nil
		}
		l.pending = append(l.pending, *e.ToolCall)
		return nil

	case ModelDone:
		return l.turnEnded(e)

	case ModelError:
		l.state = Idle
		l.pending, l.answered = nil, nil
		return []Action{{Kind: ShowError, Err: e.Err}, {Kind: AwaitInput}}

	case ToolResult:
		return l.toolReturned(e)

	case BudgetAnswer:
		return l.budgetAnswered(e)

	case Interrupt:
		// Back to idle WITHOUT sending. An interrupt that still sent the
		// turn would spend money the user just asked not to spend.
		l.state = Idle
		l.pending, l.answered = nil, nil
		return []Action{{Kind: AwaitInput}}
	}
	return nil
}

// turnEnded runs the pending tool calls, or hands back to the user.
func (l *Loop) turnEnded(e Event) []Action {
	if len(l.pending) == 0 {
		l.state = Idle
		return []Action{{Kind: AwaitInput}}
	}

	if l.used+len(l.pending) > l.maxTools {
		// THE PENDING CALLS ARE KEPT. The budget is a guard against a
		// runaway loop, not a verdict on the work: the model has already
		// decided what to do next and discarding that means re-running
		// everything it established to get there.
		//
		// proven-by: TestLoop_TheBudgetAsksAndAContinueRunsThePendingCalls
		// proven-by: TestLoop_AskingKeepsThePendingCalls
		l.state = Asking
		return []Action{{
			Kind:    AskToContinue,
			Pending: len(l.pending),
			Used:    l.used,
			Budget:  l.maxTools,
		}}
	}

	return l.runPending()
}

// budgetAnswered resumes or stops, according to the operator.
//
// proven-by: TestLoop_TheBudgetAsksAndAContinueRunsThePendingCalls
// proven-by: TestLoop_DecliningTheBudgetStopsAsItAlwaysDid
// proven-by: TestLoop_ABudgetAnswerNobodyAskedForIsIgnored
func (l *Loop) budgetAnswered(e Event) []Action {
	if l.state != Asking {
		// An answer to a question that was not asked, ignored rather
		// than obeyed. // proven-by: TestLoop_ABudgetAnswerNobodyAskedForIsIgnored
		return nil
	}
	if !e.Allow {
		l.state = Idle
		n := len(l.pending)
		l.pending, l.answered = nil, nil
		return []Action{
			{Kind: ShowError, Err: fmt.Errorf(
				"stopped: this turn would make %d more tool calls and the budget "+
					"for one input is %d. Ask again to continue", n, l.maxTools)},
			{Kind: AwaitInput},
		}
	}

	// ANOTHER BUDGET'S WORTH, not unlimited, so the question comes round
	// again at a predictable interval. // proven-by: TestLoop_ContinuingGrantsOneMoreBudgetRatherThanRemovingIt
	// A yes that removed the ceiling would leave the runaway the budget
	// exists to catch unbounded for the rest of the input.
	//
	// proven-by: TestLoop_ContinuingGrantsOneMoreBudgetRatherThanRemovingIt
	// proven-by: TestLoop_AGrantOfItsOwnSizeIsHonoured
	if e.Grant > 0 {
		l.maxTools += e.Grant
	} else {
		l.maxTools += l.granted()
	}
	return l.runPending()
}

// granted is one budget's worth: whatever the run was started with.
func (l *Loop) granted() int {
	if l.budget > 0 {
		return l.budget
	}
	return l.maxTools
}

// runPending dispatches the calls the model asked for.
func (l *Loop) runPending() []Action {
	// The assistant's own turn is recorded BEFORE the results, because the
	// model's next turn has to see that it asked.
	l.messages = append(l.messages, Message{Role: "assistant", ToolCalls: l.pending})
	l.state = Running
	l.used += len(l.pending)

	out := make([]Action, 0, len(l.pending))
	for i := range l.pending {
		out = append(out, Action{Kind: RunTool, Call: &l.pending[i]})
	}
	return out
}

// toolReturned records one result and resumes once all have returned.
func (l *Loop) toolReturned(e Event) []Action {
	if l.state != Running || e.ToolCall == nil {
		return nil
	}
	l.answered = append(l.answered, Message{
		Role: "tool", ToolCallID: e.ToolCall.ID, Content: e.Result,
	})
	if len(l.answered) < len(l.pending) {
		return nil
	}

	l.messages = append(l.messages, l.answered...)
	l.pending, l.answered = nil, nil
	l.state = Awaiting
	return []Action{{Kind: SendTurn, Send: l.conversation()}}
}

// conversation returns a copy.
// proven-by: TestLoop_TheSentConversationIsACopy
func (l *Loop) conversation() []Message {
	out := make([]Message, len(l.messages))
	copy(out, l.messages)
	return out
}

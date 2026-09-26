package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mikelear/leartech-go-common/pkg/aigateway"
)

// Streamer is the gateway seam, narrowed to what the runner needs.
// proven-by: TestRunner_AStreamWithNoTerminalEventStillEndsTheTurn
type Streamer interface {
	ChatStream(ctx context.Context, req aigateway.ChatRequest) (<-chan aigateway.StreamChunk, error)
}

// Runner drives a Loop: it turns Actions into I/O and I/O back into Events.
//
// ALL THE I/O IS HERE AND NONE OF IT IS IN THE LOOP. That split is what
// makes a scripted run and an interactive run the same code path — the
// runner changes its ports, not its logic.
type Runner struct {
	Model      string
	Client     Streamer
	Tools      *Registry
	Out        io.Writer
	Transcript *Transcript

	// UI carries everything that is not the answer. Nil means Quiet.
	UI UI

	// Intercept gets each line before the model does, so the command
	// layer can own slash commands without the loop learning what a
	// command is. Returning true means the line was handled.
	//
	// proven-by: TestRunner_AnInterceptedLineNeverReachesTheModel_Library
	Intercept func(line string) (handled bool, err error)

	// AskContinue is asked when an input reaches its tool budget, and
	// reports whether to allow one more budget's worth.
	//
	// NIL MEANS NO, which is what a scripted run needs: nobody is at the
	// keyboard, so a shell that waited would hang and a shell that
	// assumed yes would remove the ceiling from every unattended run.
	// The interactive shell supplies a real prompt.
	//
	// The int is how many more calls to permit; zero declines. A
	// number rather than a yes lets one answer cover a long piece of
	// work instead of the same question every few calls.
	//
	// proven-by: TestRunner_TheBudgetQuestionWithNobodyToAskIsANo
	// proven-by: TestRunner_AnsweringTheBudgetQuestionResumesTheWork
	// proven-by: TestRunner_ALargeGrantCarriesThroughTheWholeTurn
	AskContinue func(pending, used, budget int) int

	// MaxTokens caps each reply. Zero leaves it to the model.
	MaxTokens int

	// OnUsage is told what each turn cost, once per turn, when the
	// gateway reports it.
	//
	// OBSERVABILITY, NOT CONVERSATION STATE — so it does not become an
	// Event. Feeding it through the Loop would make a state machine that
	// decides what to send next also carry metering, and a provider that
	// reports no usage would look like a missing turn.
	//
	// NIL IS FINE and means nobody is watching. A supplier that reports
	// nothing leaves it uncalled, which is how absent stays
	// distinguishable from zero. // proven-by: TestRunner_ASupplierThatReportsNoUsageIsNotAnError
	//
	// proven-by: TestRunner_ReportsWhatTheTurnCost
	// proven-by: TestRunner_ASupplierThatReportsNoUsageIsNotAnError
	OnUsage func(aigateway.ChatUsage)
}

// Run reads one input line at a time until the reader ends.
//
// proven-by: TestRunner_APlainTurnPrintsTheAnswer
// proven-by: TestRunner_ARequestedToolRunsAndTheAnswerFollows
// proven-by: TestRunner_AnUnknownToolIsAnsweredNotFatal
// proven-by: TestRunner_ScriptedAndInteractiveTakeTheSamePath
//
// A TURN THAT ERRORED MAKES THE RUN FAIL, and the failure is reported at
// the end rather than acted on when it happens. Both halves matter. The
// first: a scripted run whose every turn was refused otherwise reads to a
// caller as a success, because the error went to stdout and the exit status
// did not. The second: aborting early under --script and continuing when
// interactive would make the two paths decide differently, which is the one
// thing scripted mode may not do.
//
// proven-by: TestRunner_AnErroredTurnFailsTheRun
// proven-by: TestRunner_AnErroredTurnStillRunsTheRestOfTheScript
func (r *Runner) Run(ctx context.Context, in Lines, system string, maxTools int) error {
	loop := New(system, maxTools)
	if r.UI == nil {
		r.UI = Quiet{}
	}

	var failures []error
	for {
		line, err := in.ReadLine()
		switch {
		case errors.Is(err, ErrInterrupted):
			// Ctrl-C on a half-typed line. The line goes, the session
			// stays — losing the session here would also lose the
			// conversation, which is the expensive part.
			continue
		case errors.Is(err, io.EOF):
			// The ordinary end of both a script and a session.
		case err != nil:
			return err
		}
		if errors.Is(err, io.EOF) {
			break
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r.Intercept != nil {
			handled, iErr := r.Intercept(line)
			if iErr != nil {
				return iErr
			}
			if handled {
				continue
			}
		}
		shown, dErr := r.drive(ctx, loop, Event{Kind: UserInput, Text: line})
		if dErr != nil {
			return dErr
		}
		failures = append(failures, shown...)
	}
	if n := len(failures); n > 0 {
		// The error itself, not a count standing in for it. A caller told
		// "1 turn failed" has to go to the transcript to learn it was a 401
		// they could have fixed by logging in.
		return fmt.Errorf("%d of this run's turns failed, the last with: %w", n, failures[n-1])
	}
	return nil
}

// drive applies an event and carries out every action it produces,
// including those produced by the actions themselves.
//
// A QUEUE RATHER THAN RECURSION. A tool result produces a SendTurn which
// produces more actions; recursion would make the depth of that chain the
// stack depth, and the chain is bounded by the model's behaviour rather
// than ours.
//
// It returns the errors it displayed, so Run can fail the process without
// changing anything it printed. Collected here rather than remembered by
// the Loop: the Loop is a pure state machine, and the last thing that went
// wrong is not state it needs to make a decision.
func (r *Runner) drive(ctx context.Context, loop *Loop, first Event) ([]error, error) {
	queue := []Event{first}
	var failed []error

	// A model that narrates before calling a tool ("I'll read the file.")
	// resumes after the result with more text, and the two ran together:
	// "I'll read the file.Router cannot see...". Measured on deepseek.
	//
	// Local to one input, not a Runner field: the join only happens WITHIN
	// a turn, so carrying the state further would be inventing a
	// relationship between separate inputs.
	//
	// proven-by: TestRunner_NarrationBeforeAToolDoesNotRunIntoTheAnswer
	// proven-by: TestRunner_TextAlreadyEndingInANewlineGainsNoSecond
	needsBreak := false

	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]

		for _, a := range loop.Step(e) {
			if err := ctx.Err(); err != nil {
				return failed, err
			}
			_ = r.Transcript.Record(a, loop.State())

			switch a.Kind {
			case ShowText:
				r.UI.FirstOutput()
				// A FAILED WRITE STOPS THE SHELL. If stdout has closed —
				// a piped reader gone, a terminal killed — continuing means
				// spending tokens on output nobody receives.
				if _, err := fmt.Fprint(r.Out, a.Text); err != nil {
					return failed, fmt.Errorf("writing the answer: %w", err)
				}
				if a.Text != "" {
					needsBreak = !strings.HasSuffix(a.Text, "\n")
				}
			case ShowReasoning:
				// Not printed by default. A caller that wants thinking reads
				// the transcript; putting it in the answer stream is the
				// merge the whole design avoids.
			case ShowError:
				r.UI.FirstOutput()
				failed = append(failed, a.Err)
				if _, err := fmt.Fprintf(r.Out, "\n%v\n", a.Err); err != nil {
					return failed, fmt.Errorf("writing an error: %w", err)
				}
			case AskToContinue:
				r.UI.FirstOutput()
				if needsBreak {
					if _, err := fmt.Fprintln(r.Out); err != nil {
						return failed, fmt.Errorf("writing: %w", err)
					}
					needsBreak = false
				}
				grant := 0
				if r.AskContinue != nil {
					grant = r.AskContinue(a.Pending, a.Used, a.Budget)
				}
				queue = append(queue, Event{
					Kind: BudgetAnswer, Allow: grant > 0, Grant: grant,
				})
			case AwaitInput:
				if _, err := fmt.Fprintln(r.Out); err != nil {
					return failed, fmt.Errorf("writing: %w", err)
				}
			case RunTool:
				r.UI.FirstOutput()
				// The same break as before a turn. A model that narrates
				// before calling ("I'll create it:") otherwise runs
				// straight into the tool line, which is where the answer
				// stops and the machine starts.
				// proven-by: TestRunner_NarrationBeforeAToolLineIsSeparated
				if needsBreak {
					if _, err := fmt.Fprintln(r.Out); err != nil {
						return failed, fmt.Errorf("writing: %w", err)
					}
					needsBreak = false
				}
				queue = append(queue, r.runTool(a.Call))
			case SendTurn:
				if needsBreak {
					if _, err := fmt.Fprintln(r.Out); err != nil {
						return failed, fmt.Errorf("writing: %w", err)
					}
					needsBreak = false
				}
				r.UI.TurnStarted()
				evs, err := r.send(ctx, a.Send)
				if err != nil {
					return failed, err
				}
				queue = append(queue, evs...)
			}
		}
	}
	return failed, nil
}

// runTool executes one call and turns the outcome into an Event.
func (r *Runner) runTool(call *ToolCall) Event {
	r.UI.ToolStarted(call.Name, call.Arguments)
	out, ok := r.Tools.Run(call.Name, call.Arguments)
	detail := ""
	if !ok {
		// Registry.Run returns the failure text as the output, which is
		// what goes to the model; the operator should see it too.
		detail = out
	}
	r.UI.ToolFinished(call.Name, ok, len(out), detail)
	// A pull request the model opened is the one thing in a tool
	// result an operator is likely to act on, and the summary hides it.
	// proven-by: TestRunner_APullRequestInToolOutputReachesTheScreen
	if links := PullRequestLinks(out); len(links) > 0 {
		r.UI.ToolLinks(links)
	}
	_ = r.Transcript.RecordToolResult(call.Name, ok, out)
	return Event{Kind: ToolResult, ToolCall: call, Result: out, ResultOK: ok}
}

// send streams one turn and collects the events it produced.
//
// COLLECTED RATHER THAN APPLIED AS THEY ARRIVE. Feeding each chunk into the
// loop mid-stream would let the loop act — run a tool, send another turn —
// while this function is still reading the response body of the turn that
// asked for it.
func (r *Runner) send(ctx context.Context, msgs []Message) ([]Event, error) {
	req := aigateway.ChatRequest{
		Model:     r.Model,
		Messages:  toWire(msgs),
		MaxTokens: r.MaxTokens,
	}
	if defs := r.definitions(); defs != nil {
		req.Tools = defs
	}

	ch, err := r.Client.ChatStream(ctx, req)
	if err != nil {
		return []Event{{Kind: ModelError, Err: err}}, nil
	}

	var out []Event
	for c := range ch {
		switch c.Kind {
		case aigateway.StreamText:
			out = append(out, Event{Kind: ModelText, Text: c.Text})
		case aigateway.StreamReasoning:
			out = append(out, Event{Kind: ModelReasoning, Text: c.Text})
		case aigateway.StreamToolCall:
			out = append(out, Event{Kind: ModelToolCall, ToolCall: &ToolCall{
				ID: c.ToolCall.ID, Name: c.ToolCall.Name, Arguments: c.ToolCall.Arguments,
			}})
		case aigateway.StreamDone:
			out = append(out, Event{Kind: ModelDone, Finish: c.Finish})
		case aigateway.StreamError:
			out = append(out, Event{Kind: ModelError, Err: c.Err})
		case aigateway.StreamUsage:
			// NOT an Event. The usage frame arrives AFTER the finish
			// reason, so appending it here would put a chunk beyond the
			// end of the turn into the loop's queue.
			// proven-by: TestRunner_ReportsWhatTheTurnCost
			if r.OnUsage != nil && c.Usage != nil {
				r.OnUsage(*c.Usage)
			}
		}
	}
	// A stream that closed with no terminal event still has to end the
	// turn, or the loop waits forever for a Done that is not coming.
	if !endsTheTurn(out) {
		out = append(out, Event{Kind: ModelDone, Finish: "incomplete"})
	}
	return out, nil
}

func endsTheTurn(evs []Event) bool {
	for _, e := range evs {
		if e.Kind == ModelDone || e.Kind == ModelError {
			return true
		}
	}
	return false
}

// definitions renders the registry as OpenAI function tools.
func (r *Runner) definitions() json.RawMessage {
	if r.Tools == nil || len(r.Tools.Names()) == 0 {
		return nil
	}
	type fn struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	type def struct {
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	defs := make([]def, 0, len(r.Tools.Names()))
	for _, name := range r.Tools.Names() {
		t := r.Tools.tools[name]
		defs = append(defs, def{Type: "function", Function: fn{
			Name: t.Name, Description: t.Description, Parameters: t.Schema,
		}})
	}
	b, err := json.Marshal(defs)
	if err != nil {
		return nil
	}
	return b
}

// toWire converts the loop's messages to the gateway's shape, marking the
// END OF THE CONVERSATION as the cacheable prefix.
//
// WHY THIS MATTERS MORE HERE THAN ANYWHERE ELSE. Every tool result sends
// the WHOLE conversation again — see Loop.toolReturned, which appends and
// then emits SendTurn with the entire history. A session with forty tool
// calls re-sends every earlier tool result forty times, and tool results
// here carry file contents.
//
// THE TAIL, NOT THE SYSTEM MESSAGE. Marking the system prompt was measured
// useless on 2026-09-26: the built-in prompt is 765 characters, the cluster
// serves none by default (SHELL_SYSTEM_PROMPT is empty), and a provider
// floor of ~1024 TOKENS meant the marking did not apply at all. It was also
// the wrong target — the system prompt is the one part that does not grow.
// Marking the last message makes the whole history so far the prefix, so
// the next turn reads it back instead of paying input rate for it, and the
// system prompt is inside that prefix for free.
//
// NO SIZE GATE. A prefix below the provider's floor is simply not cached
// and reported as such; the client does not need to know each supplier's
// minimum to decide, and holding that table was how the old gate came to
// be wrong. cache_control is an optimisation HINT — source:
// leartech-ai-gateway internal/adapter/ollama.go, which serves rather than
// rejects it and leaves CacheReported false.
//
// proven-by: TestToWire_MarksTheEndOfTheConversation
// proven-by: TestToWire_MarksTheTailEvenWhenItIsShort
// proven-by: TestToWire_MarksExactlyOneMessage
// proven-by: TestToWire_SkipsAMessageWithNoTextToMark
// proven-by: TestToWire_MarksNothingWhenThereIsNothingToSend
func toWire(msgs []Message) []aigateway.ChatRequestMessage {
	mark := lastMarkable(msgs)
	out := make([]aigateway.ChatRequestMessage, 0, len(msgs))
	for i, m := range msgs {
		w := aigateway.ChatRequestMessage{
			Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			w.ToolCalls = renderCalls(m.ToolCalls)
		}
		if i == mark {
			// Blocks and Content are alternatives — the block carries
			// the text AND the attribute, so leaving Content set sends
			// it twice. // proven-by: TestToWire_MarksTheEndOfTheConversation
			w.Content = ""
			w.Blocks = []aigateway.ContentBlock{{
				Type: "text", Text: m.Content,
				CacheControl: &aigateway.CacheControl{Type: "ephemeral"},
			}}
		}
		out = append(out, w)
	}
	return out
}

// lastMarkable is the index of the final message whose text can carry the
// breakpoint, or -1 when none can.
//
// A MESSAGE WITH NO TEXT CANNOT HOLD ONE. // proven-by: TestToWire_SkipsAMessageWithNoTextToMark
// An assistant turn that only
// called a tool has empty Content, and a block with an empty string is not
// a prefix — marking it would spend the breakpoint on nothing and cache
// less than the turn before, so the search walks back to real text.
//
// proven-by: TestToWire_SkipsAMessageWithNoTextToMark
func lastMarkable(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Content != "" {
			return i
		}
	}
	return -1
}

// renderCalls emits an assistant turn's tool calls in OpenAI's shape.
func renderCalls(calls []ToolCall) json.RawMessage {
	type fn struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type call struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	out := make([]call, 0, len(calls))
	for _, c := range calls {
		out = append(out, call{ID: c.ID, Type: "function",
			Function: fn{Name: c.Name, Arguments: string(c.Arguments)}})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

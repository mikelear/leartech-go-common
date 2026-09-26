package agentloop

import "encoding/json"

// UI is everything a caller shows that is NOT the model's answer.
//
// SEPARATE FROM Out ON PURPOSE. The answer is the product and goes to
// stdout, where it can be piped into something else. Progress, tool
// activity and status are for the person watching, and a pipeline that
// swallowed them would still work. Mixing the two means the first script
// that consumes this output has to parse spinners out of it.
//
// Quiet is the implementation for when there is no terminal, and it is the
// one an unattended run uses. // proven-by: TestQuiet_ShowsNothingThroughEveryMethod
// A terminal implementation is the consumer's: rendering is not the loop's
// concern, which is why this is an interface and not a struct.
type UI interface {
	// TurnStarted says a request is in flight, before any byte comes back.
	// This is the gap the first caller had: input accepted, nothing shown, no way
	// to tell a slow model from a dead one.
	TurnStarted()

	// FirstOutput is the moment the answer begins. Whatever TurnStarted
	// drew has to be gone by the time this returns.
	FirstOutput()

	// ToolStarted names what is about to run locally, with its arguments.
	// A tool running invisibly on someone's machine is the thing they will
	// most want to have seen.
	ToolStarted(name string, args json.RawMessage)

	// ToolFinished reports the outcome. size is the length of the
	// result; detail is the failure text when ok is false.
	//
	// The reason reaches the screen, not only the model. // proven-by: TestRegistry_AFailingToolKeepsItsOutput
	// A bare "failed" cost four approvals against a server answering
	// none of them, and a retry, because the text explaining it went
	// only into the next prompt.
	ToolFinished(name string, ok bool, size int, detail string)

	// ToolLinks reports pull requests or issues the result mentioned.
	//
	// SEPARATE FROM THE RESULT, because the result is summarised as a
	// character count and a URL inside it therefore reaches nobody. A
	// model that opens a pull request should not have to describe it in
	// prose for the operator to be able to click it.
	//
	// proven-by: TestRunner_APullRequestInToolOutputReachesTheScreen
	ToolLinks(urls []string)

	// Note is for the caller's own remarks: refusals, hints, status.
	Note(format string, a ...any)

	// Highlight is Note for something the operator should not skim
	// past — a consumer with a terminal is expected to make it visually
	// distinct, and Quiet discards it like everything else.
	//
	// proven-by: TestQuiet_ShowsNothingThroughEveryMethod
	Highlight(format string, a ...any)

	// Progress is a transient line, replaced by whatever comes next.
	//
	// SEPARATE FROM Note BECAUSE IT IS NOT A RECORD. "connecting to
	// plan" matters while it is happening and is noise once it has;
	// leaving it in the scrollback would bury the five lines that do
	// matter.
	Progress(format string, a ...any)
}

// Quiet is the UI for a pipe: it shows nothing at all.
//
// A script's stdout should contain the answer and nothing else, so this
// is the correct rendering rather than a degraded one.
type Quiet struct{}

// TurnStarted shows nothing.
func (Quiet) TurnStarted() {}

// FirstOutput shows nothing.
func (Quiet) FirstOutput() {}

// ToolStarted shows nothing.
func (Quiet) ToolStarted(string, json.RawMessage) {}

// ToolFinished shows nothing.
func (Quiet) ToolFinished(string, bool, int, string) {}

// ToolLinks shows nothing.
func (Quiet) ToolLinks([]string) {}

// Note shows nothing.
func (Quiet) Note(string, ...any) {}

// Highlight shows nothing.
func (Quiet) Highlight(string, ...any) {}

// Progress shows nothing.
func (Quiet) Progress(string, ...any) {}

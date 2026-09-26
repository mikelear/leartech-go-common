// Package agentloop is the estate's agent loop: turns, tools and a gate.
//
// WHAT IT IS. A model is given a set of tools, asked something, and
// allowed to call those tools and be told the results until it answers.
// That is all an "agent" is, and this package is the whole of it: the
// state machine ([Loop]), the thing that drives it ([Runner]), the tool
// registry and the approval gate ([Registry], [Policy], [Approver]), the
// local tools (read, list, glob, write, bash), MCP mounting, and a JSONL
// transcript of what happened.
//
// WHY IT IS HERE RATHER THAN IN A SERVICE. It was written inside
// leartech-ba-service for `ship-proven shell`, and then a second caller
// appeared: the agents the orchestrator-controller spawns, one image per
// language. Each language image needs the same loop with a different
// toolchain around it, and leartech-agent-base's own Dockerfile states the
// rule that follows from that — "Build matrix scales by adding
// Dockerfiles, not by adding conditionals". A loop living in a private
// service would have made every new language image a change to that
// service; here, an image builds it from a public module in a builder
// stage and adding a language is one Dockerfile.
//
// It is the same move [github.com/mikelear/leartech-go-common/pkg/aigateway]
// already made, and for the same reason.
//
// WHAT IS DELIBERATELY NOT HERE. Terminal rendering: the line editor, the
// history file, the spinner, the status bar. [UI] and [Lines] are
// interfaces, this package ships the non-terminal implementations
// ([Quiet], [PipeLines]), and a consumer with a terminal supplies its own.
// The loop has no opinion about how anything looks, which is what lets the
// same code serve an interactive shell and a Job with nobody watching.
//
// Session registration, slash commands and model selection are likewise a
// consumer's business — [Runner.Intercept] is the hook for the second, and
// an intercepted line never reaches the model. // proven-by: TestRunner_AnInterceptedLineNeverReachesTheModel_Library
//
// THE GATE IS NOT OPTIONAL AND NOT ADVISORY. Every tool call passes
// through [Policy] before it runs, including MCP calls, and the decision
// is made in one place rather than inside each tool — when the rooting
// lived in the tools, glob remembered to skip .git and read_file did not. // proven-by: TestRegistry_EveryCallPassesTheGate
//
// An Ask with nobody to answer is a refusal rather than an invented yes. // proven-by: TestRegistry_AnAskWithNobodyToAskIsRefused
// An unattended caller pre-seeds the answers it is willing to give; it
// does not widen the gate.
//
// USAGE. Build a [Registry], govern it, and hand it to a [Runner] along
// with an [aigateway.Client] and a [Lines]:
//
//	tools := agentloop.NewRegistry()
//	tools.Add(agentloop.ReadFile(root))
//	tools.Govern(policy, approver)
//
//	r := &agentloop.Runner{Model: m, Client: c, Tools: tools, Out: os.Stdout}
//	err := r.Run(ctx, agentloop.NewPipeLines(brief), systemPrompt, maxTools)
//
// A nil UI is [Quiet]; a nil AskContinue is a refusal, which is what an
// unattended run needs — see [Runner].
package agentloop

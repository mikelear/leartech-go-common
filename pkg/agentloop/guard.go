package agentloop

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Effect is what a tool does to the machine it runs on.
//
// DECLARED BY THE TOOL, JUDGED BY THE POLICY. The alternative — a gate
// that knows tool names — needs editing every time a tool is added, and
// the edit that is forgotten is the hole. Naming the effect means a new
// tool is governed by rules written before it existed.
//
// There is deliberately no zero value that means anything: a tool with no
// declared effect is refused at registration rather than assumed safe.
// A silent default is how 223 cluster steps ended up with no kind and an
// unanswerable question about what they were.
//
// proven-by: TestRegistry_RefusesAToolThatDeclaresNoEffect
type Effect string

const (
	// Reads looks at the machine without changing it.
	Reads Effect = "reads"
	// Writes changes files.
	Writes Effect = "writes"
	// Executes runs something, which can do anything the user can.
	Executes Effect = "executes"
)

// Verdict is what the policy decided.
type Verdict int

const (
	// Allow runs without asking.
	Allow Verdict = iota
	// Ask runs only if someone says yes.
	Ask
	// Deny does not run.
	Deny
)

func (v Verdict) String() string {
	switch v {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	}
	return "unknown"
}

// Request is one call, as the policy sees it.
type Request struct {
	Tool   string
	Effect Effect
	Args   json.RawMessage
}

// Rule judges one request. An empty reason is fine for Allow; Ask and
// Deny carry the reason because it is shown to whoever answers.
type Rule func(Request) (Verdict, string)

// Policy is the gate, composed of rules.
//
// MOST RESTRICTIVE WINS, so adding a rule can only ever tighten. The
// alternative — first match, or last match — makes the ORDER of
// registration load-bearing, and a rule added at the wrong end silently
// loosens the gate instead of tightening it.
//
// proven-by: TestPolicy_TheMostRestrictiveRuleWins
// proven-by: TestPolicy_OrderOfRegistrationDoesNotMatter
type Policy struct {
	rules []Rule
}

// NewPolicy composes rules into a gate.
func NewPolicy(rules ...Rule) *Policy { return &Policy{rules: rules} }

// Add registers another rule.
func (p *Policy) Add(r Rule) { p.rules = append(p.rules, r) }

// Decide returns the strictest verdict any rule reached.
//
// AN EMPTY POLICY DENIES EVERYTHING. Fail-closed: a gate that allows by
// default is one forgotten wiring away from no gate at all, and the
// symptom is silence.
//
// proven-by: TestPolicy_AnEmptyPolicyDenies
func (p *Policy) Decide(req Request) (Verdict, string) {
	if len(p.rules) == 0 {
		return Deny, "no policy is configured, so nothing is permitted"
	}
	worst, why := Allow, ""
	matched := false
	for _, rule := range p.rules {
		v, reason := rule(req)
		matched = true
		if v > worst {
			worst, why = v, reason
		}
	}
	if !matched {
		return Deny, "no rule covered this call"
	}
	return worst, why
}

// ForEffect builds a rule that gives one verdict to one effect and
// leaves every other effect alone.
//
// THE FACTORY THE GATE IS MADE OF. A policy is assembled from these
// rather than written as a switch, so the shape of the decision is data
// and a new effect does not mean editing a chain of conditionals.
//
// proven-by: TestForEffect_OnlyJudgesItsOwnEffect
func ForEffect(e Effect, v Verdict, reason string) Rule {
	return func(req Request) (Verdict, string) {
		if req.Effect != e {
			return Allow, ""
		}
		return v, reason
	}
}

// ForEffectReason is ForEffect with a reason computed per request.
//
// THE REASON IS SHOWN AT THE MOMENT OF DECIDING, so a wrong one is
// worse than none. A single string per effect class was fine while
// only local tools existed; once MCP tools joined the same classes,
// every remote tool inherited "it changes a file on this machine" —
// which for a read on someone else's server is simply false.
//
// The per-tool reason FUNCTION is supplied by the caller — this package
// provides the hook and calls it with the request, so a consumer can say
// "it changes a file on this machine" for a local tool and something true
// for a remote one.
//
// proven-by: TestForEffectReason_DescribesTheToolNotTheClass
func ForEffectReason(e Effect, v Verdict, reason func(Request) string) Rule {
	return func(req Request) (Verdict, string) {
		if req.Effect != e {
			return Allow, ""
		}
		return v, reason(req)
	}
}

// ServerOf returns the MCP server behind a qualified tool name, or ""
// for a local tool.
//
// proven-by: TestServerOf_NamesTheServerOrNothing
func ServerOf(tool string) string {
	rest, found := strings.CutPrefix(tool, "mcp__")
	if !found {
		return ""
	}
	server, _, ok := strings.Cut(rest, "__")
	if !ok {
		return ""
	}
	return server
}

// ForTool builds a rule aimed at one named tool, for the case a single
// tool needs treating differently from its effect class.
//
// proven-by: TestForTool_OnlyJudgesItsOwnTool
func ForTool(name string, v Verdict, reason string) Rule {
	return func(req Request) (Verdict, string) {
		if req.Tool != name {
			return Allow, ""
		}
		return v, reason
	}
}

// ReadOnly is the policy a shell has until told otherwise: look, do not
// touch, do not run.
func ReadOnly() *Policy {
	return NewPolicy(
		ForEffect(Reads, Allow, ""),
		ForEffect(Writes, Deny, "this shell is read-only"),
		ForEffect(Executes, Deny, "this shell is read-only"),
	)
}

// Approver answers an Ask.
//
// SEPARATE FROM THE POLICY because they fail differently. The policy is
// deterministic and identical everywhere; the answer depends on who is
// there. A scripted run has nobody to ask. // proven-by: TestRegistry_AnAskWithNobodyToAskIsRefused
// Inventing a yes there is how an unattended session does something
// nobody agreed to.
type Approver interface {
	// Approve is called only for Ask. The reason is the policy's, and
	// is shown to whoever answers.
	Approve(req Request, reason string) bool
}

// DenyAll answers no. The correct approver when nobody is watching.
//
// proven-by: TestRegistry_AnAskWithNobodyToAskIsRefused
type DenyAll struct{}

// Approve refuses. // proven-by: TestRegistry_AnAskWithNobodyToAskIsRefused
func (DenyAll) Approve(Request, string) bool { return false }

// AllowAll answers yes without asking, for a caller that has already
// accepted the risk explicitly.
type AllowAll struct{}

// Approve accepts. // proven-by: TestRegistry_AGovernedWriteIsRefusedUntilApproved
func (AllowAll) Approve(Request, string) bool { return true }

// ApproverFunc adapts a function.
type ApproverFunc func(Request, string) bool

// Approve calls the function.
func (f ApproverFunc) Approve(r Request, reason string) bool { return f(r, reason) }

// refusal is what the model is told when the gate says no.
//
// THE MODEL READS THIS, so it says what was refused and why. "permission
// denied" invites a retry with a different path; naming the rule does not.
func refusal(req Request, reason string) string {
	if reason == "" {
		reason = "the policy does not permit it"
	}
	return fmt.Sprintf("refused: %s (%s) — %s", req.Tool, req.Effect, reason)
}

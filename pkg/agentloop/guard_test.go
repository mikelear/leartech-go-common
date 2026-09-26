package agentloop

import (
	"encoding/json"
	"strings"
	"testing"
)

func req(tool string, e Effect) Request {
	return Request{Tool: tool, Effect: e, Args: json.RawMessage(`{}`)}
}

// The strictest rule wins, so adding one can only tighten the gate.
func TestPolicy_TheMostRestrictiveRuleWins(t *testing.T) {
	p := NewPolicy(
		ForEffect(Executes, Allow, ""),
		ForEffect(Executes, Ask, "running things needs a yes"),
		ForEffect(Executes, Deny, "this shell does not run things"),
	)
	v, reason := p.Decide(req("local__bash", Executes))
	if v != Deny {
		t.Errorf("verdict = %v, want deny — the strictest rule must win", v)
	}
	if reason == "" {
		t.Error("a refusal with no reason cannot be acted on")
	}
}

// Registration order is not load-bearing.
//
// If it were, a rule added at the wrong end would silently LOOSEN the
// gate, and the symptom would be something running that should not have.
func TestPolicy_OrderOfRegistrationDoesNotMatter(t *testing.T) {
	strictFirst := NewPolicy(
		ForEffect(Writes, Deny, "no"),
		ForEffect(Writes, Allow, ""),
	)
	looseFirst := NewPolicy(
		ForEffect(Writes, Allow, ""),
		ForEffect(Writes, Deny, "no"),
	)
	a, _ := strictFirst.Decide(req("w", Writes))
	b, _ := looseFirst.Decide(req("w", Writes))
	if a != b {
		t.Errorf("order changed the verdict: %v vs %v", a, b)
	}
	if a != Deny {
		t.Errorf("verdict = %v, want deny either way", a)
	}
}

// An empty policy denies. Fail-closed: a gate that allows by default is
// one forgotten wiring away from no gate at all.
func TestPolicy_AnEmptyPolicyDenies(t *testing.T) {
	v, reason := NewPolicy().Decide(req("anything", Reads))
	if v != Deny {
		t.Errorf("verdict = %v, want deny from an unconfigured policy", v)
	}
	if !strings.Contains(reason, "no policy") {
		t.Errorf("reason = %q, want it to name the missing configuration", reason)
	}
}

// A rule aimed at one effect leaves the others alone.
func TestForEffect_OnlyJudgesItsOwnEffect(t *testing.T) {
	r := ForEffect(Executes, Deny, "no running")
	if v, _ := r(req("x", Reads)); v != Allow {
		t.Errorf("a reads call got %v from an executes rule", v)
	}
	if v, _ := r(req("x", Executes)); v != Deny {
		t.Errorf("an executes call got %v, want deny", v)
	}
}

// A rule aimed at one tool leaves the others alone.
func TestForTool_OnlyJudgesItsOwnTool(t *testing.T) {
	r := ForTool("local__bash", Ask, "confirm")
	if v, _ := r(req("local__read_file", Reads)); v != Allow {
		t.Errorf("an unrelated tool got %v", v)
	}
	if v, _ := r(req("local__bash", Executes)); v != Ask {
		t.Errorf("the named tool got %v, want ask", v)
	}
}

// A tool that never said what it does is refused at registration.
//
// Defaulting it would govern it by a rule written for something else,
// and the mistake would be invisible until it ran.
func TestRegistry_RefusesAToolThatDeclaresNoEffect(t *testing.T) {
	r := NewRegistry()
	err := r.Add(Tool{Name: "local__mystery", Run: func(json.RawMessage) (string, error) {
		return "", nil
	}})
	if err == nil {
		t.Fatal("a tool with no declared effect was registered")
	}
	if len(r.Names()) != 0 {
		t.Errorf("names = %v, want nothing registered", r.Names())
	}
}

// Out of the box a registry reads and nothing else.
func TestRegistry_TheDefaultPolicyIsReadOnly(t *testing.T) {
	r := NewRegistry()
	ran := false
	for _, e := range []Effect{Writes, Executes} {
		if err := r.Add(Tool{Name: "t_" + string(e), Effect: e,
			Run: func(json.RawMessage) (string, error) { ran = true; return "did it", nil }}); err != nil {
			t.Fatal(err)
		}
		out, ok := r.Run("t_"+string(e), nil)
		if ok {
			t.Errorf("%s ran under the default policy", e)
		}
		if !strings.Contains(out, "read-only") {
			t.Errorf("out = %q, want the reason named", out)
		}
	}
	if ran {
		t.Error("a governed tool executed despite being refused")
	}
}

// Every call passes the gate, including a reading one.
func TestRegistry_EveryCallPassesTheGate(t *testing.T) {
	var seen []Request
	r := NewRegistry()
	r.Govern(NewPolicy(func(q Request) (Verdict, string) {
		seen = append(seen, q)
		return Allow, ""
	}), nil)
	if err := r.Add(Tool{Name: "local__read_file", Effect: Reads,
		Run: func(json.RawMessage) (string, error) { return "ok", nil }}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Run("local__read_file", json.RawMessage(`{"path":"x"}`)); !ok {
		t.Fatal("an allowed call did not run")
	}
	if len(seen) != 1 {
		t.Fatalf("the gate saw %d calls, want 1", len(seen))
	}
	if seen[0].Tool != "local__read_file" || seen[0].Effect != Reads {
		t.Errorf("gate saw %+v, want the tool and its effect", seen[0])
	}
	if string(seen[0].Args) != `{"path":"x"}` {
		t.Errorf("gate saw args %s, want the real arguments — it cannot judge without them",
			seen[0].Args)
	}
}

// An Ask runs only when someone says yes, and the reason reaches them.
func TestRegistry_AGovernedWriteIsRefusedUntilApproved(t *testing.T) {
	ran := 0
	mk := func(answer bool) *Registry {
		r := NewRegistry()
		r.Govern(
			NewPolicy(ForEffect(Reads, Allow, ""), ForEffect(Writes, Ask, "it changes a file")),
			ApproverFunc(func(_ Request, reason string) bool {
				if !strings.Contains(reason, "changes a file") {
					t.Errorf("the approver was given reason %q", reason)
				}
				return answer
			}),
		)
		if err := r.Add(Tool{Name: "local__write_file", Effect: Writes,
			Run: func(json.RawMessage) (string, error) { ran++; return "written", nil }}); err != nil {
			t.Fatal(err)
		}
		return r
	}

	if out, ok := mk(false).Run("local__write_file", nil); ok {
		t.Error("a declined write ran anyway")
	} else if !strings.Contains(out, "declined") {
		t.Errorf("out = %q, want the decline visible to the model", out)
	}
	if ran != 0 {
		t.Fatalf("the tool ran %d times despite being declined", ran)
	}

	if _, ok := mk(true).Run("local__write_file", nil); !ok {
		t.Error("an approved write did not run")
	}
	if ran != 1 {
		t.Errorf("the tool ran %d times after approval, want 1", ran)
	}
}

// With nobody to ask, an Ask is a refusal.
//
// A scripted run has no one at the terminal. Inventing a yes there is
// how an unattended session does something nobody agreed to.
func TestRegistry_AnAskWithNobodyToAskIsRefused(t *testing.T) {
	r := NewRegistry()
	r.Govern(NewPolicy(ForEffect(Executes, Ask, "it runs a command")), DenyAll{})
	ran := false
	if err := r.Add(Tool{Name: "local__bash", Effect: Executes,
		Run: func(json.RawMessage) (string, error) { ran = true; return "", nil }}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Run("local__bash", nil); ok {
		t.Error("a command ran with nobody to approve it")
	}
	if ran {
		t.Error("the tool body executed")
	}
}

// A per-request reason describes the tool, not its class.
func TestForEffectReason_DescribesTheToolNotTheClass(t *testing.T) {
	r := ForEffectReason(Writes, Ask, func(q Request) string { return "about " + q.Tool })
	v, reason := r(req("mcp__srv__thing", Writes))
	if v != Ask {
		t.Errorf("verdict = %v, want ask", v)
	}
	if reason != "about mcp__srv__thing" {
		t.Errorf("reason = %q, want it computed from the request", reason)
	}
	// And it leaves other effects alone.
	if v, _ := r(req("x", Reads)); v != Allow {
		t.Errorf("a reads call got %v", v)
	}
}

// ServerOf names the server behind a qualified tool, or nothing.
func TestServerOf_NamesTheServerOrNothing(t *testing.T) {
	cases := map[string]string{
		"mcp__deepwiki__ask_question": "deepwiki",
		"mcp__whoami__whoami":         "whoami",
		"local__read_file":            "",
		"mcp__broken":                 "",
		"":                            "",
	}
	for tool, want := range cases {
		if got := ServerOf(tool); got != want {
			t.Errorf("ServerOf(%q) = %q, want %q", tool, got, want)
		}
	}
}

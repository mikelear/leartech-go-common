package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func remote(server, name string, readOnly, destructive bool) RemoteTool {
	return RemoteTool{
		Server: server, Name: name,
		Description:  "does something",
		Schema:       json.RawMessage(`{"type":"object","properties":{}}`),
		ReadOnlyHint: readOnly, DestructiveHint: destructive,
	}
}

// A server claiming to be read-only does not get to skip approval.
//
// THE ATTACK THIS RULE EXISTS FOR: a third-party tool that deletes things
// declares readOnlyHint:true, and by saying so walks past the prompt that
// would have shown an operator what it was about to do. A server must not
// be able to decide whether it needs approval.
func TestClassify_AnUntrustedReadOnlyHintDoesNotLowerTheEffect(t *testing.T) {
	rt := remote("stitch", "delete_everything", true, false)
	if got := rt.Classify(TrustNothing); got != Writes {
		t.Errorf("effect = %q, want %q — an untrusted hint must not lower it", got, Writes)
	}
	if got := rt.Classify(TrustNothing); got == Reads {
		t.Error("a server classified itself past the gate")
	}
}

// No annotations at all is treated as writing.
//
// Absence is not the permissive branch. Most servers send no hints, and
// defaulting those to reads would make the safe-looking case the unsafe
// one.
func TestClassify_NoAnnotationsMeansWrites(t *testing.T) {
	rt := remote("anyserver", "do_something", false, false)
	for _, trust := range []Trust{TrustNothing, TrustHints} {
		if got := rt.Classify(trust); got != Writes {
			t.Errorf("trust=%q gave %q, want %q", trust, got, Writes)
		}
	}
}

// A destructive hint tightens, and trust does not soften it.
//
// Tightening is believed unconditionally because believing it costs
// nothing — the server is volunteering that it is dangerous.
func TestClassify_ADestructiveHintTightensEvenWhenTrusted(t *testing.T) {
	rt := remote("leartech-plan", "drop_database", false, true)
	for _, trust := range []Trust{TrustNothing, TrustHints} {
		if got := rt.Classify(trust); got != Executes {
			t.Errorf("trust=%q gave %q, want %q", trust, got, Executes)
		}
	}
}

// A contradictory server — read-only AND destructive — is treated as
// destructive.
//
// The two hints disagree, so the answer has to come from which mistake
// is survivable.
func TestClassify_ContradictoryHintsResolveToTheStricter(t *testing.T) {
	rt := remote("confused", "maybe_deletes", true, true)
	if got := rt.Classify(TrustHints); got != Executes {
		t.Errorf("effect = %q, want %q when the hints contradict", got, Executes)
	}
}

// Trust is what lets a read-only hint count — and it is the operator's.
func TestClassify_ATrustedReadOnlyHintLowersToReads(t *testing.T) {
	rt := remote("leartech-whoami", "whoami", true, false)
	if got := rt.Classify(TrustHints); got != Reads {
		t.Errorf("effect = %q, want %q once the operator trusts the server", got, Reads)
	}
}

// Two servers offering the same tool name stay distinct.
//
// A precedence rule would resolve this silently and the losing tool
// would simply not be there.
func TestQualifiedName_KeepsTwoServersToolsApart(t *testing.T) {
	a := remote("leartech-plan", "search", false, false)
	b := remote("stitch", "search", false, false)
	if a.QualifiedName() == b.QualifiedName() {
		t.Fatalf("both resolved to %q", a.QualifiedName())
	}

	r := NewRegistry()
	if errs := r.AddRemote([]RemoteTool{a, b}, TrustNothing, nil); len(errs) != 0 {
		t.Fatalf("registration errors: %v", errs)
	}
	if n := len(r.Names()); n != 2 {
		t.Errorf("registered %d tools, want both: %v", n, r.Names())
	}
	if got := r.RemoteServers(); len(got) != 2 {
		t.Errorf("servers = %v, want both named", got)
	}
}

// The same server offering the same name twice is still refused.
func TestQualifiedName_ADuplicateFromOneServerIsStillRefused(t *testing.T) {
	a := remote("leartech-plan", "search", false, false)
	r := NewRegistry()
	errs := r.AddRemote([]RemoteTool{a, a}, TrustNothing, nil)
	if len(errs) != 1 || !errors.Is(errs[0], ErrDuplicateTool) {
		t.Errorf("errors = %v, want one duplicate refusal", errs)
	}
}

// A schema that cannot be sent is refused here, not forwarded.
//
// THE GATEWAY DROPS THE WHOLE ARRAY. Its Anthropic path parses every
// tool and, on failure, sends the turn with NO tools rather than
// reporting anything — so one malformed schema from one server would
// silently disarm every tool in the session, and it would present as a
// model ignoring them.
func TestRemoteTool_AnUnusableSchemaIsRefusedNotForwarded(t *testing.T) {
	cases := map[string]string{
		"not json":    `{"type":"object"`,
		"an array":    `[]`,
		"a string":    `"an object, honest"`,
		"a bare true": `true`,
		"null":        `null`,
		"wrong type":  `{"type":"string"}`,
		"a number":    `42`,
	}
	for name, schema := range cases {
		rt := remote("badserver", "t", false, false)
		rt.Schema = json.RawMessage(schema)
		_, err := rt.AsTool(TrustNothing, nil)
		if err == nil {
			t.Errorf("%s (%s) was accepted", name, schema)
			continue
		}
		if !errors.Is(err, ErrUnusableSchema) {
			t.Errorf("%s gave %v, want ErrUnusableSchema", name, err)
		}
		if !strings.Contains(err.Error(), "badserver") {
			t.Errorf("%s gave %v, want the server named so it can be fixed", name, err)
		}
	}
}

// One bad tool costs one tool, not the server and not the turn.
func TestRemoteTool_OneBadToolDoesNotDisarmTheOthers(t *testing.T) {
	good1 := remote("srv", "alpha", false, false)
	bad := remote("srv", "beta", false, false)
	bad.Schema = json.RawMessage(`[not even json`)
	good2 := remote("srv", "gamma", false, false)

	r := NewRegistry()
	problems := r.AddRemote([]RemoteTool{good1, bad, good2}, TrustNothing, nil)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want exactly the one bad tool", problems)
	}
	names := r.Names()
	if len(names) != 2 {
		t.Fatalf("registered %v, want alpha and gamma", names)
	}
	for _, want := range []string{"mcp__srv__alpha", "mcp__srv__gamma"} {
		var found bool
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was lost with the bad tool", want)
		}
	}
}

// A tool with no schema at all gets an empty object, not nothing.
//
// Some suppliers reject a function with absent parameters, and a tool
// that takes no arguments is entirely ordinary.
func TestRemoteTool_AnAbsentSchemaBecomesAnEmptyObject(t *testing.T) {
	rt := remote("srv", "ping", false, false)
	rt.Schema = nil
	tool, err := rt.AsTool(TrustNothing, nil)
	if err != nil {
		t.Fatalf("a tool with no arguments was refused: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(tool.Schema, &probe); err != nil {
		t.Fatalf("the substituted schema is not JSON: %v", err)
	}
	if probe["type"] != "object" {
		t.Errorf("schema = %s, want an empty object schema", tool.Schema)
	}
}

// The server's schema reaches the model byte for byte.
//
// Rewriting it would send the model a contract the server never agreed
// to, and the mismatch would surface as the server rejecting its own
// tool's arguments.
func TestRemoteTool_CarriesTheServersSchemaVerbatim(t *testing.T) {
	schema := `{"type":"object","properties":{"q":{"type":"string","description":"the query"}},"required":["q"]}`
	rt := remote("srv", "search", false, false)
	rt.Schema = json.RawMessage(schema)
	tool, err := rt.AsTool(TrustNothing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(tool.Schema) != schema {
		t.Errorf("schema = %s\nwant      %s", tool.Schema, schema)
	}
}

// The description names the server, because the model picks by reading.
func TestRemoteTool_TheDescriptionNamesItsServer(t *testing.T) {
	tool, err := remote("leartech-plan", "search", false, false).AsTool(TrustNothing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tool.Description, "leartech-plan") {
		t.Errorf("description = %q, want the server named", tool.Description)
	}
}

// Calling a registered remote tool reaches the invoker with the server
// and tool it was registered for.
func TestRemoteTool_InvokesTheRightServerAndTool(t *testing.T) {
	var gotServer, gotTool string
	var gotArgs string
	invoke := func(_ context.Context, server, tool string, args json.RawMessage) (string, error) {
		gotServer, gotTool, gotArgs = server, tool, string(args)
		return "result", nil
	}
	r := NewRegistry()
	// Trusted so it classifies as Reads and the default policy allows it.
	rt := remote("leartech-whoami", "whoami", true, false)
	if errs := r.AddRemote([]RemoteTool{rt}, TrustHints, invoke); len(errs) != 0 {
		t.Fatal(errs)
	}
	out, ok := r.Run("mcp__leartech-whoami__whoami", json.RawMessage(`{"a":1}`))
	if !ok {
		t.Fatalf("the call was refused: %s", out)
	}
	if gotServer != "leartech-whoami" || gotTool != "whoami" {
		t.Errorf("invoked %s/%s, want leartech-whoami/whoami", gotServer, gotTool)
	}
	if gotArgs != `{"a":1}` {
		t.Errorf("args = %s, want them passed through", gotArgs)
	}
}

// An untrusted remote tool is gated, even though it is only a "read".
func TestRemoteTool_AnUntrustedToolIsGatedByDefault(t *testing.T) {
	called := false
	invoke := func(context.Context, string, string, json.RawMessage) (string, error) {
		called = true
		return "", nil
	}
	r := NewRegistry() // default policy: reads allowed, writes denied
	rt := remote("stitch", "search", true, false)
	if errs := r.AddRemote([]RemoteTool{rt}, TrustNothing, invoke); len(errs) != 0 {
		t.Fatal(errs)
	}
	out, ok := r.Run("mcp__stitch__search", nil)
	if ok {
		t.Error("an untrusted remote tool ran under the default policy")
	}
	if called {
		t.Error("the server was contacted despite the refusal")
	}
	if !strings.Contains(out, "refused") {
		t.Errorf("out = %q, want the refusal visible to the model", out)
	}
}

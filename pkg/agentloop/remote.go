package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Trust is how much of what a server says about itself is believed.
//
// Set by the operator, per server. // proven-by: TestClassify_AnUntrustedReadOnlyHintDoesNotLowerTheEffect
// A server that could classify itself as harmless would be deciding
// whether it needs approval, which is the approval's whole job.
type Trust string

const (
	// TrustNothing believes the schema and the name, and nothing else.
	TrustNothing Trust = ""
	// TrustHints believes readOnlyHint, because the operator said to.
	TrustHints Trust = "hints"
)

// RemoteTool is what a server said about one of its tools, in the only
// terms the gate cares about.
//
// A LOCAL SHAPE RATHER THAN THE SDK'S, so the classification rules can be
// tested without a server, a transport or a protocol version — and so
// this package does not depend on the MCP SDK to decide a policy question.
type RemoteTool struct {
	Server      string
	Name        string
	Description string
	Schema      json.RawMessage

	// ReadOnlyHint and DestructiveHint are the server's own claims.
	ReadOnlyHint    bool
	DestructiveHint bool
}

// Classify decides what effect a remote tool has.
//
// Hints tighten unconditionally and loosen only where an operator has
// vouched for the server. // proven-by: TestClassify_AnUntrustedReadOnlyHintDoesNotLowerTheEffect
// A third-party tool that deletes things, declaring readOnlyHint:true,
// would otherwise walk past the prompt by asserting it deserves to.
// Absence is not the permissive branch: no annotations means writing.
//
// proven-by: TestClassify_AnUntrustedReadOnlyHintDoesNotLowerTheEffect
// proven-by: TestClassify_NoAnnotationsMeansWrites
// proven-by: TestClassify_ADestructiveHintTightensEvenWhenTrusted
// proven-by: TestClassify_ATrustedReadOnlyHintLowersToReads
func (rt RemoteTool) Classify(trust Trust) Effect {
	// Tightening is unconditional: a server volunteering that it is
	// dangerous is believed, because believing it costs nothing.
	if rt.DestructiveHint {
		return Executes
	}
	if rt.ReadOnlyHint && trust == TrustHints {
		return Reads
	}
	// Everything else, including a read-only claim from a server the
	// operator has not vouched for.
	return Writes
}

// QualifiedName is the name the model sees.
//
// mcp__<server>__<tool>, matching local__<tool>. Two servers offering
// `search` is ordinary, and a precedence rule would resolve it silently —
// the losing tool simply would not be there.
//
// proven-by: TestQualifiedName_KeepsTwoServersToolsApart
func (rt RemoteTool) QualifiedName() string {
	return "mcp__" + rt.Server + "__" + rt.Name
}

// ErrUnusableSchema reports a tool whose schema is not sendable. // proven-by: TestRemoteTool_AnUnusableSchemaIsRefusedNotForwarded
//
// THE REASON THIS IS A HARD REFUSAL: the gateway's Anthropic path parses
// the whole tools array and, on failure, sends the turn with NO TOOLS AT
// ALL rather than reporting anything (ai-gateway, translateTools). One
// malformed schema from one server would silently disarm every tool in
// the session, and the symptom is a model that appears to ignore them.
// Refusing the single bad tool here keeps the rest of the turn armed.
//
// proven-by: TestRemoteTool_AnUnusableSchemaIsRefusedNotForwarded
// proven-by: TestRemoteTool_OneBadToolDoesNotDisarmTheOthers
var ErrUnusableSchema = errors.New("agentloop: a remote tool's schema cannot be sent to a model")

// Invoke calls a tool on a server.
type Invoke func(ctx context.Context, server, tool string, args json.RawMessage) (string, error)

// AsTool turns a remote tool into one the registry can hold.
//
// proven-by: TestRemoteTool_CarriesTheServersSchemaVerbatim
// proven-by: TestRemoteTool_TheDescriptionNamesItsServer
func (rt RemoteTool) AsTool(trust Trust, invoke Invoke) (Tool, error) {
	if rt.Server == "" || rt.Name == "" {
		return Tool{}, fmt.Errorf("%w: a tool needs a server and a name", ErrUnusableSchema)
	}
	schema, err := usableSchema(rt.Schema)
	if err != nil {
		return Tool{}, fmt.Errorf("%w: %s: %v", ErrUnusableSchema, rt.QualifiedName(), err)
	}

	desc := rt.Description
	if desc == "" {
		desc = "a tool offered by " + rt.Server
	}
	// The server is named in the description as well as the tool name,
	// because the model chooses between tools by reading these and
	// "search" from two servers is otherwise a coin toss.
	desc = fmt.Sprintf("[%s] %s", rt.Server, desc)

	server, name := rt.Server, rt.Name
	return Tool{
		Name:        rt.QualifiedName(),
		Description: desc,
		Schema:      schema,
		Effect:      rt.Classify(trust),
		Run: func(args json.RawMessage) (string, error) {
			if invoke == nil {
				return "", errors.New("no connection to " + server)
			}
			return invoke(context.Background(), server, name, args)
		},
	}, nil
}

// usableSchema checks what a model actually requires of a tool schema.
//
// An object, and parseable. Anything else is refused rather than
// repaired. // proven-by: TestRemoteTool_AnUnusableSchemaIsRefusedNotForwarded
// Guessing at what a server meant would hand the model a contract
// nobody agreed to.
func usableSchema(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		// A tool taking no arguments is ordinary; say so explicitly
		// rather than sending nothing, which some suppliers reject.
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("it is not valid JSON: %w", err)
	}
	obj, ok := probe.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("it is %s, and a tool schema has to be an object",
			describeJSON(probe))
	}
	if t, present := obj["type"]; present {
		if s, isString := t.(string); !isString || s != "object" {
			return nil, fmt.Errorf(`its "type" is %v, and a tool schema has to be an object`, t)
		}
	}
	return raw, nil
}

func describeJSON(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case float64:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "an array"
	}
	return "not an object"
}

// AddRemote registers a server's tools, skipping unsendable ones. // proven-by: TestRemoteTool_OneBadToolDoesNotDisarmTheOthers
//
// ONE BAD TOOL COSTS ONE TOOL. Refusing the whole server for a single
// malformed schema would let one server break another's tools, and
// accepting it would disarm the turn at the gateway.
//
// proven-by: TestRemoteTool_OneBadToolDoesNotDisarmTheOthers
func (r *Registry) AddRemote(tools []RemoteTool, trust Trust, invoke Invoke) []error {
	var problems []error
	for _, rt := range tools {
		t, err := rt.AsTool(trust, invoke)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if err := r.Add(t); err != nil {
			problems = append(problems, err)
		}
	}
	return problems
}

// RemoteServers lists the servers behind the registered tools, in the
// order they were registered.
func (r *Registry) RemoteServers() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range r.Names() {
		if !strings.HasPrefix(n, "mcp__") {
			continue
		}
		rest := strings.TrimPrefix(n, "mcp__")
		server, _, found := strings.Cut(rest, "__")
		if !found || seen[server] {
			continue
		}
		seen[server] = true
		out = append(out, server)
	}
	return out
}

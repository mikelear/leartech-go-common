package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A service that builds its verifier only when an issuer is configured reaches
// RequireScope with a nil *Verifier. Before the guard this was a nil pointer
// dereference — "invalid memory address", naming neither the scope nor the
// cause — while every other misuse of this function panics with a sentence
// telling you what to do. The panic is not the bug; the useless panic is.
func TestRequireScopeOnANilVerifierSaysWhatWentWrong(t *testing.T) {
	var v *Verifier

	defer func() {
		r := recover()
		require.NotNil(t, r, "a nil Verifier must not be silently accepted: the route would be wired to a gate that cannot check anything")

		msg, ok := r.(string)
		require.True(t, ok, "panicked with %T (%v), not a diagnostic string — a runtime error here means the guard is missing", r, r)

		require.NotContains(t, msg, "invalid memory address",
			"still a nil dereference rather than a diagnosis")
		require.Contains(t, msg, "nil *Verifier",
			"the message must name the cause")
		require.Contains(t, msg, "artifact:read",
			"the message must name the scope, so the offending route is findable")
	}()

	_ = v.RequireScope("artifact:read")
	t.Fatal("expected a panic")
}

// The guard must not swallow the empty-scope case: an empty scope on a nil
// verifier is still first and foremost an empty scope.
func TestNilVerifierGuardStillReportsTheScopeItWasGiven(t *testing.T) {
	var v *Verifier
	defer func() {
		msg, _ := recover().(string)
		require.True(t, strings.Contains(msg, "nil *Verifier"), "got: %s", msg)
	}()
	_ = v.RequireScope("")
	t.Fatal("expected a panic")
}

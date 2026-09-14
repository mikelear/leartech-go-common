package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A resource server keeps three lists of its own scopes, in three places:
// KnownScopes (code), what RequireScope gates (code), and ScopesSupported
// (LEARTECH_AUTH_SCOPES_SUPPORTED, an env var). UngatedScopes ties the first
// two together. Nothing tied the third to anything, and each pairing fails
// silently and differently — so each gets its own test with a control.

func TestGatedScopes_ReportsWhatRoutesActuallyEnforce(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{KnownScopes: Scopes{scopeRead, scopeWrite}})

	assert.Empty(t, v.GatedScopes(),
		"before a route is wired nothing is enforced; a non-empty answer here "+
			"would mean the set is being read from the declaration rather than from use")

	v.RequireScope(scopeRead)

	assert.Equal(t, Scopes{scopeRead}, v.GatedScopes(),
		"read is gated by a route")
	assert.Equal(t, Scopes{scopeWrite}, v.UngatedScopes(),
		"and the two halves stay complementary")
}

// The dangerous one: the service enforces a scope it never advertised. A caller
// cannot discover what it needs — discovery omits it, and the WWW-Authenticate
// hint is built from ScopesSupported so it omits it too. The 403 is unactionable.
func TestScopeSurface_FindsAScopeEnforcedButNeverAdvertised(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{
		KnownScopes:     Scopes{scopeRead, scopeWrite},
		ScopesSupported: []string{string(scopeRead)},
	})
	v.RequireScope(scopeRead)
	v.RequireScope(scopeWrite) // enforced, but absent from ScopesSupported

	s := v.ScopeSurface()

	assert.False(t, s.Empty(), "the lists disagree")
	assert.Equal(t, Scopes{scopeWrite}, s.GatedNotPublished,
		"write is enforced and undiscoverable")
	assert.Empty(t, s.DeclaredNotGated, "both declared scopes are gated")
	assert.Empty(t, s.PublishedNotGated, "the one published scope is gated")
	assert.Contains(t, s.String(), "unactionable",
		"the message must name the consequence, not just the difference")
}

// The over-granting one: discovery tells clients to request a scope that gates
// nothing, so they carry an extra scope for no reason.
func TestScopeSurface_FindsAnAdvertisedScopeThatGatesNothing(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{
		KnownScopes:     Scopes{scopeRead, scopeWrite},
		ScopesSupported: []string{string(scopeRead), string(scopeWrite)},
	})
	v.RequireScope(scopeRead)

	s := v.ScopeSurface()

	assert.Equal(t, []string{string(scopeWrite)}, s.PublishedNotGated,
		"write is advertised and gates nothing")
	assert.Equal(t, Scopes{scopeWrite}, s.DeclaredNotGated,
		"the same scope is also declared-and-unused; both are true and both are reported")
	assert.Empty(t, s.GatedNotPublished)
}

// The control. Every scope declared, gated and advertised — the surface agrees
// and Empty() says so. Without this, a ScopeSurface that always reported a
// disagreement would pass all the tests above.
func TestScopeSurface_AgreesWhenTheThreeListsMatch(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{
		KnownScopes:     Scopes{scopeRead, scopeWrite},
		ScopesSupported: []string{string(scopeRead), string(scopeWrite)},
	})
	v.RequireScope(scopeRead)
	v.RequireScope(scopeWrite)

	s := v.ScopeSurface()

	require.True(t, s.Empty(), "expected agreement, got: %s", s)
	assert.Contains(t, s.String(), "agrees")
	assert.ElementsMatch(t, Scopes{scopeRead, scopeWrite}, v.GatedScopes())
}

// A service with no ScopesSupported configured at all. Every gated scope is
// then undiscoverable — this is the default shape, so it must be reported
// rather than excused as "not configured yet".
func TestScopeSurface_NoAdvertisedScopesMeansEveryGateIsUndiscoverable(t *testing.T) {
	v, _ := gateFixture(t, VerifierConfig{KnownScopes: Scopes{scopeRead}})
	v.RequireScope(scopeRead)

	s := v.ScopeSurface()

	assert.Equal(t, Scopes{scopeRead}, s.GatedNotPublished,
		"an unset scopes_supported is not a pass; the caller still cannot discover the scope")
	assert.False(t, s.Empty())
}

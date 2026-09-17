package auth

import "testing"

// isTokenAllowedAccess is documented as depending ONLY on the claims and the
// required permissions — "never on JWKS / issuer / audience state" — and as
// being shared verbatim between the ServiceClient and Verifier middleware
// paths. Both halves of that were prose until now.
//
// It matters because the two middleware paths look independent. If they ever
// diverged, a service would authorise differently depending on which
// constructor it happened to use, and nothing would say so: both would still
// return 200 for the happy path.

// The function is a free function precisely so it cannot read verifier state.
// This pins the decision table itself, so a change to the rule is visible as a
// changed expectation rather than as a behaviour nobody wrote down.
func TestIsTokenAllowedAccess_IgnoresVerifierState(t *testing.T) {
	cases := []struct {
		name     string
		required Permissions
		scopes   Scopes
		perms    Permissions
		want     bool
	}{
		{"empty required admits anything", nil, Scopes{}, nil, true},
		{"empty required admits anything, explicit", Permissions{}, Scopes{ScopeAPI}, nil, true},
		{"internal_services short-circuits without any permission",
			Permissions{PermAdmin}, Scopes{ScopeInternalServices}, nil, true},
		{"api scope plus the permission",
			Permissions{PermUser}, Scopes{ScopeAPI}, Permissions{PermUser}, true},
		{"api scope without the permission is refused",
			Permissions{PermAdmin}, Scopes{ScopeAPI}, Permissions{PermUser}, false},
		{"permission without the api scope is refused",
			Permissions{PermUser}, Scopes{}, Permissions{PermUser}, false},
		{"no scopes and no permissions is refused",
			Permissions{PermUser}, Scopes{}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := &TokenClaims{Scopes: tc.scopes, Permissions: tc.perms}
			if got := isTokenAllowedAccess(tc.required, claims); got != tc.want {
				t.Errorf("isTokenAllowedAccess(%v, scopes=%v perms=%v) = %v, want %v",
					tc.required, tc.scopes, tc.perms, got, tc.want)
			}
		})
	}
}

// The ServiceClient method is documented as the twin of the free function.
// Asserting they agree is what makes "shared verbatim" checkable: if one grows
// a branch the other lacks, this fails rather than the estate discovering it
// as an inconsistent 403.
func TestBothMiddlewarePathsShareOneDecision(t *testing.T) {
	c := &ServiceClient{}
	cases := []struct {
		required Permissions
		scopes   Scopes
		perms    Permissions
	}{
		{nil, Scopes{}, nil},
		{Permissions{PermAdmin}, Scopes{ScopeInternalServices}, nil},
		{Permissions{PermAdmin}, Scopes{ScopeAPI}, Permissions{PermUser}},
		{Permissions{PermUser}, Scopes{ScopeAPI}, Permissions{PermUser}},
		{Permissions{PermUser}, Scopes{}, Permissions{PermUser}},
	}
	for _, tc := range cases {
		claims := &TokenClaims{Scopes: tc.scopes, Permissions: tc.perms}
		free := isTokenAllowedAccess(tc.required, claims)
		method := c.isTokenAllowedAccess(tc.required, claims)
		if free != method {
			t.Errorf("the two paths disagree for required=%v scopes=%v perms=%v: "+
				"free=%v method=%v. A service would then authorise differently "+
				"depending on which constructor it used.",
				tc.required, tc.scopes, tc.perms, free, method)
		}
	}
}

// An empty permission set is the whole reason authgate exists: go-common
// returns true before it reads the claims, so the route authorises nothing.
// Pinned here so the behaviour the estate now gates against is written down
// where the behaviour lives.
func TestEmptyRequiredPermsAdmitsATokenWithNothing(t *testing.T) {
	claims := &TokenClaims{Scopes: Scopes{}, Permissions: nil}
	if !isTokenAllowedAccess(Permissions{}, claims) {
		t.Fatal("an empty required set refused a token with no scopes and no permissions; " +
			"the documented behaviour is that it admits everything, which is why " +
			"Middleware(nil) is the absence of a gate rather than a weak one")
	}
}

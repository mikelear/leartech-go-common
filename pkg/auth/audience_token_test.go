package auth

// Per-callee tokens.
//
// Config.TargetAudience is a single value, so a ServiceClient can address one
// callee. Measured 2026-09-12: leartech-maestro-service delivers to every
// registered consumer through one TokenGetter and had no TargetAudience at all,
// so its delivery tokens carried no `aud` — and a consumer therefore cannot
// enforce its own audience without breaking delivery. Any service holding any
// valid platform token can post into any consumer's /consume endpoint.
//
// The load-bearing tests here are the two that a naive implementation passes
// and a correct one must too:
//
//	TestGetAuthTokenForAudience_DifferentAudiencesGetDifferentTokens — sharing
//	one oauth2 source across audiences hands out a token minted for the WRONG
//	callee, and that is invisible until a callee starts enforcing.
//
//	TestGetAuthTokenForAudience_EmptyAudienceIsRefused — falling back to
//	TargetAudience, or minting audience-less, recreates the exact bug: Hydra
//	returns such a token happily and every enforcing callee refuses it.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// audienceStub is a Hydra stand-in that records every requested audience and
// returns a distinct token per audience, so a cached-wrong-token bug shows up
// as the wrong string rather than as a silent pass.
type audienceStub struct {
	mu        sync.Mutex
	requested []string
	srv       *httptest.Server
}

func newAudienceStub(t *testing.T) *audienceStub {
	t.Helper()
	s := &audienceStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			_, _ = w.Write([]byte(`{"keys":[]}`))
		case "/health/ready":
			w.WriteHeader(http.StatusOK)
		case "/oauth2/token":
			_ = r.ParseForm()
			aud := r.Form.Get("audience")
			s.mu.Lock()
			s.requested = append(s.requested, aud)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			// Token body encodes the audience so a mix-up is visible.
			_, _ = w.Write([]byte(`{"access_token":"tok-for-` + aud +
				`","token_type":"bearer","expires_in":3600}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *audienceStub) audiences() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.requested...)
}

func newClientForAudienceTests(t *testing.T, stub *audienceStub) *ServiceClient {
	t.Helper()
	c, err := NewServiceClient(t.Context(), Config{
		ServerURL:      stub.srv.URL,
		ClientID:       "svc",
		ClientSecret:   "secret",
		Audience:       "leartech-this-service",
		TargetAudience: "leartech-default-callee",
	})
	if err != nil {
		t.Fatalf("NewServiceClient: %v", err)
	}
	sc, ok := c.(*ServiceClient)
	if !ok {
		t.Fatalf("expected *ServiceClient, got %T", c)
	}
	return sc
}

// ── the pair ────────────────────────────────────────────────────────────────

func TestGetAuthTokenForAudience_RequestsTheAudienceGiven(t *testing.T) {
	stub := newAudienceStub(t)
	c := newClientForAudienceTests(t, stub)

	tok, err := c.GetAuthTokenForAudience(t.Context(), "leartech-plan-conformance-consumer")
	if err != nil {
		t.Fatalf("GetAuthTokenForAudience: %v", err)
	}
	if *tok != "tok-for-leartech-plan-conformance-consumer" {
		t.Fatalf("got token %q — minted for the wrong audience", *tok)
	}
}

// THE ONE A NAIVE IMPLEMENTATION FAILS. oauth2's ReuseTokenSource caches the
// token it holds, so one source shared across audiences returns the FIRST
// audience's token for every subsequent callee — invisible until a callee
// enforces.
func TestGetAuthTokenForAudience_DifferentAudiencesGetDifferentTokens(t *testing.T) {
	stub := newAudienceStub(t)
	c := newClientForAudienceTests(t, stub)

	first, err := c.GetAuthTokenForAudience(t.Context(), "leartech-consumer-a")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := c.GetAuthTokenForAudience(t.Context(), "leartech-consumer-b")
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if *first == *second {
		t.Fatalf("both audiences returned %q. One oauth2 source shared across "+
			"audiences hands consumer-b a token minted for consumer-a, and nothing "+
			"notices until consumer-b enforces its audience.", *first)
	}
	if *second != "tok-for-leartech-consumer-b" {
		t.Errorf("second token is %q, want the consumer-b one", *second)
	}
}

// Refusing an empty audience matters more than it looks: falling back to
// TargetAudience would silently address the wrong callee, and minting without
// an audience produces exactly the token every enforcing callee refuses.
func TestGetAuthTokenForAudience_EmptyAudienceIsRefused(t *testing.T) {
	stub := newAudienceStub(t)
	c := newClientForAudienceTests(t, stub)

	_, err := c.GetAuthTokenForAudience(t.Context(), "")
	if err == nil {
		t.Fatal("an empty audience minted a token. It must not fall back to " +
			"TargetAudience (wrong callee, silently) nor mint audience-less (refused " +
			"by every enforcing callee, with nothing saying why).")
	}
	if !errors.Is(err, ErrEmptyAudience) {
		t.Errorf("want ErrEmptyAudience, got %v", err)
	}
	if len(stub.audiences()) != 0 {
		t.Errorf("it called the token endpoint anyway: %v", stub.audiences())
	}
}

// ── caching ─────────────────────────────────────────────────────────────────

// Per-audience sources must be REUSED, or every delivery mints afresh and a
// fan-out broker turns one event into N token requests.
func TestGetAuthTokenForAudience_CachesPerAudience(t *testing.T) {
	stub := newAudienceStub(t)
	c := newClientForAudienceTests(t, stub)

	for range 3 {
		if _, err := c.GetAuthTokenForAudience(t.Context(), "leartech-consumer-a"); err != nil {
			t.Fatalf("mint: %v", err)
		}
	}

	if got := len(stub.audiences()); got != 1 {
		t.Errorf("the token endpoint was called %d times for three mints of the same "+
			"audience; the source is not being reused: %v", got, stub.audiences())
	}
}

func TestGetAuthTokenForAudience_IsSafeUnderConcurrency(t *testing.T) {
	stub := newAudienceStub(t)
	c := newClientForAudienceTests(t, stub)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			aud := "leartech-consumer-a"
			if i%2 == 0 {
				aud = "leartech-consumer-b"
			}
			if _, err := c.GetAuthTokenForAudience(t.Context(), aud); err != nil {
				t.Errorf("concurrent mint: %v", err)
			}
		}(i)
	}
	wg.Wait()
	// Both audiences must appear; the map is shared state and this runs under
	// -race in CI.
	seen := map[string]bool{}
	for _, a := range stub.audiences() {
		seen[a] = true
	}
	if !seen["leartech-consumer-a"] || !seen["leartech-consumer-b"] {
		t.Errorf("expected both audiences to be minted, saw %v", stub.audiences())
	}
}

// ── SetAuthHeaderForAudience ────────────────────────────────────────────────

func TestSetAuthHeaderForAudience_AttachesTheRightToken(t *testing.T) {
	stub := newAudienceStub(t)
	c := newClientForAudienceTests(t, stub)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://callee/consume", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := c.SetAuthHeaderForAudience(t.Context(), req, "leartech-consumer-a"); err != nil {
		t.Fatalf("SetAuthHeaderForAudience: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok-for-leartech-consumer-a" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestSetAuthHeaderForAudience_RefusesAnEmptyAudience(t *testing.T) {
	stub := newAudienceStub(t)
	c := newClientForAudienceTests(t, stub)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://callee", nil)

	if err := c.SetAuthHeaderForAudience(t.Context(), req, ""); err == nil {
		t.Fatal("an empty audience attached a header")
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("it attached a header despite failing")
	}
}

// ── nothing that exists today changes ───────────────────────────────────────

// GetAuthToken must keep using cfg.TargetAudience. Every service in the estate
// relies on it, so the new path is additive or it is a migration.
func TestGetAuthToken_StillUsesTheConfiguredTargetAudience(t *testing.T) {
	stub := newAudienceStub(t)
	c := newClientForAudienceTests(t, stub)

	if _, err := c.GetAuthToken(t.Context()); err != nil {
		t.Fatalf("GetAuthToken: %v", err)
	}
	auds := stub.audiences()
	if len(auds) != 1 || auds[0] != "leartech-default-callee" {
		t.Fatalf("GetAuthToken requested %v, want the configured TargetAudience", auds)
	}
}

// ServiceClient must satisfy the new interface, or callers cannot depend on it.
func TestServiceClientSatisfiesAudienceTokenGetter(t *testing.T) {
	stub := newAudienceStub(t)
	var _ AudienceTokenGetter = newClientForAudienceTests(t, stub)
}

//go:build e2e

// These tests talk to a real gateway. They exist because of a defect no unit
// test in this repo could have caught: the meaning of model_allowlist is
// ai-gateway's to define, and every local test asserting it passed while
// asserting the inverse. Three of them did.
//
// A claim about another service is only provable by asking that service.
package aigateway

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Small enough that a leaked key is bounded, large enough for one exchange.
const e2eBudgetMicros = 50_000

func live(t *testing.T) (*Client, string) {
	t.Helper()
	base, token := os.Getenv("LEARTECH_GATEWAY"), os.Getenv("LEARTECH_TOKEN")
	if base == "" || token == "" {
		// Not skipped quietly: an e2e run that silently passes because it
		// was unconfigured is a green tick meaning the opposite of what it
		// says.
		t.Fatal("LEARTECH_GATEWAY and LEARTECH_TOKEN are required; this test " +
			"proves behaviour on a real gateway and cannot infer it")
	}
	return New(base, token, nil), base
}

// mintDisposable returns a key and registers its revocation.
func mintDisposable(t *testing.T, c *Client, name string, allow []string) CreateKeyResponse {
	t.Helper()
	exp := time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339)
	budget := int64(e2eBudgetMicros)
	got, err := c.CreateKey(context.Background(), CreateKeyRequest{
		Name:           name,
		ModelAllowlist: allow,
		BudgetMicros:   &budget,
		ExpiresAt:      &exp,
	})
	if err != nil {
		t.Fatalf("mint %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := c.RevokeKey(context.Background(), got.KeyID); err != nil {
			// Loud: an un-revoked key outlives the test run and bills.
			t.Errorf("REVOKE %s BY HAND: %v", got.KeyID, err)
		}
	})
	if got.Secret == "" {
		t.Fatalf("%s minted with no secret", got.KeyID)
	}
	return got
}

func firstModel(t *testing.T, c *Client) string {
	t.Helper()
	ms, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 {
		t.Fatal("the gateway advertises no models, so nothing here can be proven")
	}
	return ms[0].ID
}

// THE decisive test: an empty allowlist denies on the real gateway.
//
// source: leartech-ai-gateway migrations/00013_model_allowlist_explicit.sql
func TestE2E_AnEmptyAllowlistDeniesEveryModelOnTheRealGateway(t *testing.T) {
	c, base := live(t)
	model := firstModel(t, c)

	got := mintDisposable(t, c, "e2e-empty-allowlist", nil)
	if !got.Key.DeniesEveryModel() {
		t.Fatalf("the gateway returned a non-empty allowlist for a key minted "+
			"without one: %v", got.Key.ModelAllowlist)
	}

	_, err := New(base, got.Secret, nil).Chat(context.Background(), ChatRequest{
		Model:     model,
		Messages:  []ChatRequestMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	})
	if err == nil {
		t.Fatalf("an empty model_allowlist ALLOWED %s. If this is the real "+
			"behaviour then migration 00013 has been reverted and the CLI's "+
			"rendering, its comments and three unit tests are all now wrong "+
			"in the other direction", model)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not allowed") &&
		!strings.Contains(strings.ToLower(err.Error()), "model") {
		t.Errorf("refused, but not for the model: %v", err)
	}
}

func TestE2E_AnAllowlistedModelIsCallableAndOthersAreNot(t *testing.T) {
	c, base := live(t)
	ms, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 2 {
		t.Fatalf("need two models to prove one is allowed and another is not; "+
			"this gateway serves %d", len(ms))
	}
	allowed, denied := ms[0].ID, ms[1].ID

	got := mintDisposable(t, c, "e2e-one-model", []string{allowed})
	keyed := New(base, got.Secret, nil)

	r, err := keyed.Chat(context.Background(), ChatRequest{
		Model:     allowed,
		Messages:  []ChatRequestMessage{{Role: "user", Content: "Reply with the single word: ok"}},
		MaxTokens: 5,
	})
	if err != nil {
		t.Fatalf("the allowlisted model %s was refused: %v", allowed, err)
	}
	if _, ok := r.Reply(); !ok {
		t.Errorf("%s answered with no choices", allowed)
	}
	if r.Usage.PromptTokens == 0 {
		t.Error("a real completion reported zero prompt tokens")
	}

	if _, err := keyed.Chat(context.Background(), ChatRequest{
		Model:     denied,
		Messages:  []ChatRequestMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	}); err == nil {
		t.Fatalf("a key allowlisting only %s reached %s", allowed, denied)
	}
}

// A refusal carries the gateway's reason, not the client's guess.
func TestE2E_AModelRefusalDoesNotReadAsAScopeProblem(t *testing.T) {
	c, base := live(t)
	got := mintDisposable(t, c, "e2e-reason", []string{firstModel(t, c)})

	_, err := New(base, got.Secret, nil).Chat(context.Background(), ChatRequest{
		Model:     "definitely-not-a-model",
		Messages:  []ChatRequestMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	})
	if err == nil {
		t.Fatal("an unknown model was accepted")
	}
	if strings.Contains(err.Error(), "lacks the scope") {
		t.Errorf("a model refusal reported as a scope problem: %v", err)
	}
	if !strings.Contains(err.Error(), "definitely-not-a-model") &&
		!strings.Contains(strings.ToLower(err.Error()), "model") {
		t.Errorf("the refusal does not say it was about the model: %v", err)
	}
}

// A minted key cannot mint another: keys_write Requires MethodJWT.
func TestE2E_AVirtualKeyCannotMintAnotherKey(t *testing.T) {
	c, base := live(t)
	got := mintDisposable(t, c, "e2e-no-escalation", []string{firstModel(t, c)})

	budget := int64(e2eBudgetMicros)
	if _, err := New(base, got.Secret, nil).CreateKey(context.Background(),
		CreateKeyRequest{Name: "e2e-escalated", BudgetMicros: &budget}); err == nil {
		t.Fatal("a virtual key minted another key: the JWT-only guarantee on " +
			"keys_write is what makes a session key safe to store on disk")
	}
}

// A real gateway reports the cache fields on a real cache write.
//
// No local test can establish this. The wire shape is ai-gateway's to define,
// the cache behaviour is the supplier's, and this repo's fixtures are copies of
// what someone believed both were. The model_allowlist defect above is the
// precedent: three local tests asserted the inverse of the truth and all three
// were green.
//
// It also discriminates between deployments, which is the immediate use: the
// carry that sends cache_control through to Anthropic landed in v0.0.72, so
// this passes on a cluster running that or later and fails loudly on one that
// is behind -- rather than the CLI quietly printing an uncached-looking line.
func TestLive_ACacheWriteIsReportedOnTheWire(t *testing.T) {
	c, _ := live(t)

	// Anthropic will not create a cache entry below its minimum cacheable
	// prefix (1024 tokens for the Opus/Sonnet class). A shorter prefix is
	// accepted, cached nothing, and reports nothing -- which is
	// indistinguishable from an unsupported gateway unless the prefix is
	// known to be over the line.
	prefix := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 400)

	resp, err := c.Chat(context.Background(), ChatRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 4,
		Messages: []ChatRequestMessage{{
			Role: "user",
			Blocks: []ContentBlock{
				{Type: "text", Text: prefix, CacheControl: &CacheControl{Type: "ephemeral"}},
				{Type: "text", Text: "Reply with the single word: ok"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	u := resp.Usage

	if !u.CacheReported() {
		t.Fatalf("a %d-token prompt with cache_control produced no cache fields at all; "+
			"this gateway does not carry cache_control through (pre-v0.0.72) or the "+
			"supplier does not support it", u.PromptTokens)
	}

	written := u.CacheWrite5mTokens() + u.CacheWrite1hTokens()
	if written == 0 && u.CachedTokens() == 0 {
		t.Errorf("cache fields are present but both read and write are zero on a %d-token "+
			"cacheable prefix; cache_control reached the response shape but not the supplier",
			u.PromptTokens)
	}

	// The arithmetic has to hold on live data too, not just on fixtures. This
	// is where a convention mismatch shows up: if the gateway passed
	// Anthropic's additive counts through unchanged, the subsets would exceed
	// prompt_tokens and fresh would go negative.
	if u.FreshTokens() < 0 {
		t.Errorf("fresh is %d (prompt %d, read %d, written %d): the supplier's additive "+
			"convention was not normalised to OpenAI's subset convention",
			u.FreshTokens(), u.PromptTokens, u.CachedTokens(), written)
	}
	t.Logf("prompt=%d fresh=%d read=%d write5m=%d write1h=%d",
		u.PromptTokens, u.FreshTokens(), u.CachedTokens(),
		u.CacheWrite5mTokens(), u.CacheWrite1hTokens())
}

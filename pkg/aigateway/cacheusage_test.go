package aigateway

import (
	"encoding/json"
	"testing"
)

// The subsets must FIT inside prompt_tokens.
//
// This is the arithmetic that caught a wrong shape once already: a body
// claiming 2815 prompt with 2804 read AND 3000 written implies a fresh
// remainder of -2989. Nothing rejected it, because each field was individually
// plausible; only the sum was impossible. A client that derives fresh by
// subtraction turns that into a negative token count on a user's screen.
//
// OpenAI's convention is what makes this checkable: prompt_tokens is every
// prompt token processed, and each detail field is a subset of it. Anthropic's
// is additive -- input_tokens EXCLUDES the cache counts -- so the same three
// numbers mean different things depending on which convention is in force.
// The gateway normalises to one. This test is the assertion that it did.
func TestChatUsage_TheCacheSubsetsFitInsidePromptTokens(t *testing.T) {
	var got ChatResponse
	if err := json.Unmarshal([]byte(goldenChat), &got); err != nil {
		t.Fatal(err)
	}
	u := got.Usage

	sum := u.CachedTokens() + u.CacheWrite5mTokens() + u.CacheWrite1hTokens()
	if sum > u.PromptTokens {
		t.Errorf("the subsets total %d but prompt_tokens is only %d; they are being read as additive, not as subsets",
			sum, u.PromptTokens)
	}
	if u.FreshTokens() < 0 {
		t.Errorf("fresh is %d — a negative token count reached the surface", u.FreshTokens())
	}
	if want := u.PromptTokens + u.CompletionTokens; u.TotalTokens != want {
		t.Errorf("total_tokens is %d, want %d: the total must stay prompt+completion so it is not double-counted by the cache fields",
			u.TotalTokens, want)
	}
}

// The two write tiers are separate numbers, not one number and a flag.
//
// They are priced differently (1.25x vs 2x at the supplier), so collapsing them
// makes the bill underivable from the counts. A single "written" field with a
// TTL label alongside cannot express a call that wrote at both tiers, which the
// block-level cache_control permits.
func TestChatUsage_TheTwoWriteTiersAreDistinct(t *testing.T) {
	const both = `{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,
	  "leartech_cache":{"write_5m_tokens":30,"write_1h_tokens":70}}`
	var u ChatUsage
	if err := json.Unmarshal([]byte(both), &u); err != nil {
		t.Fatal(err)
	}
	if u.CacheWrite5mTokens() != 30 || u.CacheWrite1hTokens() != 70 {
		t.Fatalf("the tiers did not survive decoding separately: 5m=%d 1h=%d",
			u.CacheWrite5mTokens(), u.CacheWrite1hTokens())
	}
	if u.FreshTokens() != 0 {
		t.Errorf("fresh is %d, want 0: both tiers must count against the prompt", u.FreshTokens())
	}
}

// Fresh is derived, never decoded.
//
// If the gateway ever sends a "fresh_tokens" field, this client must keep
// ignoring it: a reported fresh count is a fourth number that can disagree with
// the other three, and then there is no way to tell which one is lying.
func TestChatUsage_FreshIsTheRemainderNotAReportedNumber(t *testing.T) {
	const lying = `{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,
	  "fresh_tokens":99999,
	  "prompt_tokens_details":{"cached_tokens":90}}`
	var u ChatUsage
	if err := json.Unmarshal([]byte(lying), &u); err != nil {
		t.Fatal(err)
	}
	if u.FreshTokens() != 10 {
		t.Errorf("fresh is %d, want 10 (100-90): a reported field was preferred over the arithmetic",
			u.FreshTokens())
	}
}

// ABSENT and ZERO are different facts.
//
// Absent means the supplier has no prompt cache -- Ollama. Zero means it has
// one and this call did not use it. Collapsing them makes the CLI report a 0%
// hit rate for a model with nothing to hit, which reads as a broken cache
// rather than an inapplicable one.
func TestChatUsage_AnAbsentCacheIsNotAZeroCache(t *testing.T) {
	var none ChatUsage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":30,"completion_tokens":29,"total_tokens":59}`), &none); err != nil {
		t.Fatal(err)
	}
	if none.CacheReported() {
		t.Error("a body with no cache fields reported a cache")
	}

	var idle ChatUsage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":30,"completion_tokens":29,"total_tokens":59,
	  "prompt_tokens_details":{"cached_tokens":0}}`), &idle); err != nil {
		t.Fatal(err)
	}
	if !idle.CacheReported() {
		t.Error("a supplier that reported a cache with zero hits was treated as having no cache")
	}
	if idle.CachedTokens() != 0 || idle.FreshTokens() != 30 {
		t.Errorf("cached=%d fresh=%d, want 0 and 30", idle.CachedTokens(), idle.FreshTokens())
	}
}

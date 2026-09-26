package aigateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A message with no blocks marshals content as a plain string.
//
// The ordinary path, asserted first because everything below changes it. If a
// message carrying only Content started emitting an array, every existing
// caller would change shape on the wire without changing a line.
func TestChatRequestMessage_PlainContentStaysAString(t *testing.T) {
	b, err := json.Marshal(ChatRequestMessage{Role: "user", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"content":"hello"`) {
		t.Errorf("plain content did not marshal as a string: %s", b)
	}
}

// Blocks replace the string, and the two are never sent together.
//
// Sending both would be ambiguous at the far end: the gateway rebuilds blocks
// from whichever representation it finds, so a message carrying each resolves
// to one of them silently. Asserted as the ABSENCE of the string form rather
// than the presence of the array, because an implementation that emitted both
// would satisfy a presence check.
func TestChatRequestMessage_BlocksReplaceTheStringRatherThanJoiningIt(t *testing.T) {
	m := ChatRequestMessage{
		Role:    "user",
		Content: "this must not be sent",
		Blocks: []ContentBlock{
			{Type: "text", Text: "STABLE PREFIX", CacheControl: &CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "volatile question"},
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "this must not be sent") {
		t.Errorf("both content forms were sent; the far end picks one and the "+
			"caller cannot tell which: %s", b)
	}
	if !strings.Contains(string(b), "STABLE PREFIX") {
		t.Errorf("the blocks did not reach the wire: %s", b)
	}
}

// cache_control rides on the BLOCK, not the message.
//
// A cache breakpoint marks a position in the prompt — everything up to and
// including the marked block is the cacheable prefix. Hoisting it to the
// message would make it mean "cache this request", which is a different and
// unimplementable instruction: the volatile turn must not be in the prefix or
// nothing is ever reused.
func TestCacheControl_RidesOnTheBlockNotTheMessage(t *testing.T) {
	b, _ := json.Marshal(ChatRequestMessage{
		Role: "user",
		Blocks: []ContentBlock{
			{Type: "text", Text: "prefix", CacheControl: &CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "question"},
		},
	})

	var wire struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("content did not marshal as a block array: %v\n%s", err, b)
	}
	if len(wire.Content) != 2 {
		t.Fatalf("got %d blocks, want 2", len(wire.Content))
	}
	if _, marked := wire.Content[0]["cache_control"]; !marked {
		t.Error("the first block carries no cache_control, so no prefix is marked")
	}
	if _, marked := wire.Content[1]["cache_control"]; marked {
		t.Error("the volatile block is marked cacheable. Everything up to the " +
			"breakpoint is the prefix, so marking the last block asks the provider " +
			"to cache the whole turn — which can never be reused.")
	}
}

// An omitted TTL is omitted on the wire, not defaulted by us.
//
// The tiers are priced differently: 5 minutes is the provider's default and one
// hour costs more to establish. Sending a ttl the caller did not ask for bills
// them for the dearer tier on our initiative, so absence has to travel as
// absence.
func TestCacheControl_AnOmittedTTLIsNotInvented(t *testing.T) {
	b, _ := json.Marshal(ChatRequestMessage{
		Role:   "user",
		Blocks: []ContentBlock{{Type: "text", Text: "p", CacheControl: &CacheControl{Type: "ephemeral"}}},
	})
	if strings.Contains(string(b), "ttl") {
		t.Errorf("a ttl was sent for a caller who did not choose one: %s", b)
	}

	withTTL, _ := json.Marshal(ChatRequestMessage{
		Role:   "user",
		Blocks: []ContentBlock{{Type: "text", Text: "p", CacheControl: &CacheControl{Type: "ephemeral", TTL: "1h"}}},
	})
	if !strings.Contains(string(withTTL), `"ttl":"1h"`) {
		t.Errorf("an explicit 1h ttl did not reach the wire: %s", withTTL)
	}
}

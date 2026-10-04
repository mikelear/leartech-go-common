package agentloop

import (
	"encoding/json"
	"testing"
)

// A checkpoint at a turn boundary round-trips: restore rebuilds a loop with
// the same conversation, and the same conversation hashes the same.
func TestCheckpoint_ATurnBoundaryRoundTrips(t *testing.T) {
	l := New("system prompt", 6)
	l.Step(Event{Kind: UserInput, Text: "hello"})
	l.Step(Event{Kind: ModelDone, Finish: "stop"})
	// the turn ended with no tool calls, so the loop is idle again

	cp, err := l.Checkpoint(1, "")
	if err != nil {
		t.Fatalf("checkpoint at a settled boundary: %v", err)
	}
	if len(cp.Messages) != 2 { // system + user; a reply with no content adds none
		t.Errorf("messages = %d, want 2 (system + user; a contentless reply adds none)", len(cp.Messages))
	}
	b, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	back, err := RestoreCheckpoint(b, 6)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	cp2, err := back.Checkpoint(1, "")
	if err != nil {
		t.Fatal(err)
	}
	if cp2.Hash != cp.Hash {
		t.Errorf("round-trip changed the conversation: %s != %s", cp2.Hash, cp.Hash)
	}
}

// A checkpoint mid-turn is refused: resuming it would resume a
// half-executed chain of tool calls.
func TestCheckpoint_RefusesAMidTurnState(t *testing.T) {
	l := New("s", 6)
	l.Step(Event{Kind: UserInput, Text: "go"})
	l.Step(Event{Kind: ModelToolCall, ToolCall: &ToolCall{ID: "t1", Name: "local__read_file"}})
	l.Step(Event{Kind: ModelDone, Finish: "tool_calls"})
	// state Running: the tool call is outstanding
	if _, err := l.Checkpoint(1, ""); err == nil {
		t.Error("a loop with a pending tool call checkpointed; resuming it would resume a half-finished turn")
	}
}

// Restore refuses an unknown version rather than guessing.
func TestRestoreCheckpoint_RefusesAnUnknownVersion(t *testing.T) {
	b, _ := json.Marshal(Checkpoint{Version: 99, Messages: []Message{{Role: "user"}}})
	if _, err := RestoreCheckpoint(b, 6); err == nil {
		t.Error("an unknown version restored; a future file must be refused, not guessed at")
	}
}

// An empty checkpoint restored as success would be a silent reset.
func TestRestoreCheckpoint_AnEmptyCheckpointIsAnError(t *testing.T) {
	b, _ := json.Marshal(Checkpoint{Version: 1})
	if _, err := RestoreCheckpoint(b, 6); err == nil {
		t.Error("an empty conversation restored without error")
	}
}

// A restored loop continues accumulating: the conversation is live history,
// not a display copy.
func TestRestoreCheckpoint_ContinuesAccumulating(t *testing.T) {
	l := New("s", 6)
	l.Step(Event{Kind: UserInput, Text: "one"})
	l.Step(Event{Kind: ModelDone, Finish: "stop"})
	b, _ := json.Marshal(mustCP(t, l, 1))
	back, err := RestoreCheckpoint(b, 6)
	if err != nil {
		t.Fatal(err)
	}
	acts := back.Step(Event{Kind: UserInput, Text: "two"})
	var sent []Message
	for _, a := range acts {
		if a.Kind == SendTurn {
			sent = a.Send
		}
	}
	foundTwo, foundOne := false, false
	for _, m := range sent {
		if m.Content == "two" {
			foundTwo = true
		}
		if m.Content == "one" {
			foundOne = true
		}
	}
	if !foundOne || !foundTwo {
		t.Errorf("restored conversation sent %v; want both the restored and the new turn", sent)
	}
}

// The resumed session's next checkpoint chains onto the file it resumed.
func TestCheckpoint_ARestoredSessionChainsOntoTheFileItResumed(t *testing.T) {
	l := New("s", 6)
	l.Step(Event{Kind: UserInput, Text: "one"})
	l.Step(Event{Kind: ModelDone, Finish: "stop"})
	restored := mustCP(t, l, 1)
	b, _ := json.Marshal(restored)
	back, _ := RestoreCheckpoint(b, 6)
	back.Step(Event{Kind: UserInput, Text: "two"})
	back.Step(Event{Kind: ModelDone, Finish: "stop"})
	chained, err := back.CheckpointChainedFrom(restored, 2)
	if err != nil {
		t.Fatal(err)
	}
	if chained.PrevHash != restored.Hash {
		t.Errorf("prev_hash = %s, want the restored checkpoint's %s — the chain must be continuous across the restart", chained.PrevHash, restored.Hash)
	}
}

func mustCP(t *testing.T, l *Loop, turn int) *Checkpoint {
	t.Helper()
	cp, err := l.Checkpoint(turn, "")
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	return cp
}

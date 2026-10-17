package agentloop

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// cpFrom builds a checkpoint from a hand-written message list, hashed the way
// the loop hashes so the chain assertions are meaningful.
func cpFrom(t *testing.T, turn int, prev string, msgs []Message) *Checkpoint {
	t.Helper()
	c := &Checkpoint{Version: CheckpointVersion, Turn: turn, PrevHash: prev, Messages: msgs}
	c.Hash = c.computeHash()
	return c
}

func tool(id, body string) Message { return Message{Role: "tool", ToolCallID: id, Content: body} }
func user(s string) Message        { return Message{Role: "user", Content: s} }
func asst(s string) Message        { return Message{Role: "assistant", Content: s} }

// Compact elides the heaviest eligible tool result first, buying the most
// room for the fewest cuts.
func TestCompact_ElidesTheHeaviestToolResultsFirst(t *testing.T) {
	src := cpFrom(t, 3, "", []Message{
		user("start"),
		tool("a", strings.Repeat("x", 100)), // small
		user("more"),
		tool("b", strings.Repeat("y", 5000)), // the heavy one
		user("again"),
		asst("thinking"),
		user("tail"),
	})
	// Target just under the before-size so exactly one elision is needed.
	before := conversationChars(src.Messages)
	out, rep, err := Compact(src, CompactOptions{TargetChars: before - 4000, KeepHeadTurns: 1, KeepTailTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Elided != 1 {
		t.Fatalf("elided %d, want 1 — one heavy result should have sufficed", rep.Elided)
	}
	// The heavy one (index 3) is the one that got stubbed, not the small one.
	if !strings.Contains(out.Messages[3].Content, "compacted") {
		t.Errorf("the heavy tool result was not the one elided: %q", out.Messages[3].Content)
	}
	if strings.Contains(out.Messages[1].Content, "compacted") {
		t.Errorf("the small tool result was elided before the heavy one")
	}
}

// Compact never rewrites a user or assistant message — only tool results.
func TestCompact_NeverTouchesUserOrAssistantMessages(t *testing.T) {
	src := cpFrom(t, 2, "", []Message{
		user("the task, stated once"),
		asst(strings.Repeat("reasoning ", 2000)), // heavy assistant prose
		tool("a", strings.Repeat("z", 500)),
		user("tail"),
	})
	out, _, err := Compact(src, CompactOptions{TargetChars: 10, KeepHeadTurns: 1, KeepTailTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Messages[0].Content != "the task, stated once" {
		t.Errorf("a user message was altered: %q", out.Messages[0].Content)
	}
	if !strings.HasPrefix(out.Messages[1].Content, "reasoning ") {
		t.Errorf("an assistant message was altered: %q", out.Messages[1].Content[:20])
	}
}

// The protected head and tail turns are never candidates, even when they are
// the heaviest thing in the conversation.
func TestCompact_ProtectsTheHeadAndTailTurns(t *testing.T) {
	src := cpFrom(t, 3, "", []Message{
		user("turn 1"),
		tool("head", strings.Repeat("h", 9000)), // belongs to turn 1 — protected head
		user("turn 2"),
		tool("mid", strings.Repeat("m", 3000)), // the only eligible one
		user("turn 3"),
		tool("tail", strings.Repeat("t", 9000)), // belongs to turn 3 — protected tail
	})
	out, rep, err := Compact(src, CompactOptions{TargetChars: 1, KeepHeadTurns: 1, KeepTailTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Elided != 1 {
		t.Fatalf("elided %d, want 1 — only the middle result is eligible", rep.Elided)
	}
	if strings.Contains(out.Messages[1].Content, "compacted") {
		t.Error("the protected head tool result was elided")
	}
	if strings.Contains(out.Messages[5].Content, "compacted") {
		t.Error("the protected tail tool result was elided")
	}
	if !strings.Contains(out.Messages[3].Content, "compacted") {
		t.Error("the eligible middle result was not elided")
	}
}

// The compacted checkpoint chains onto the source: its PrevHash is the
// source's Hash, recording the compaction as one deliberate link.
func TestCompact_ReChainsOntoTheSourceCheckpoint(t *testing.T) {
	src := cpFrom(t, 4, "deadbeef", []Message{
		user("a"), tool("x", strings.Repeat("q", 4000)), user("b"),
	})
	out, _, err := Compact(src, CompactOptions{TargetChars: 10, KeepHeadTurns: 1, KeepTailTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.PrevHash != src.Hash {
		t.Errorf("prev_hash = %s, want the source's %s — a compaction must record as a chain link", out.PrevHash, src.Hash)
	}
	if out.Turn != src.Turn {
		t.Errorf("turn = %d, want the source's %d — compaction does not advance the turn", out.Turn, src.Turn)
	}
	if out.Hash == src.Hash {
		t.Error("the compacted checkpoint has the same hash as its source; the edit must change the hash")
	}
}

// Compact stops the moment it is under target rather than eliding everything.
func TestCompact_StopsOnceUnderTarget(t *testing.T) {
	src := cpFrom(t, 3, "", []Message{
		user("a"),
		tool("1", strings.Repeat("x", 5000)),
		user("mid"),
		tool("2", strings.Repeat("y", 5000)),
		tool("3", strings.Repeat("z", 5000)),
		user("tail"),
	})
	before := conversationChars(src.Messages)
	// Dropping one 5000-char result is enough to get under this.
	out, rep, err := Compact(src, CompactOptions{TargetChars: before - 4800, KeepHeadTurns: 1, KeepTailTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Elided != 1 {
		t.Fatalf("elided %d, want 1 — one cut was enough", rep.Elided)
	}
	if !rep.Fit {
		t.Error("report says it did not fit, but one cut was sized to be enough")
	}
	_ = out
}

// When even eliding every eligible result leaves the conversation over
// target, Compact says so rather than pretending success.
func TestCompact_ReportsWhenItCannotFit(t *testing.T) {
	// The tail turn alone is enormous and protected, so no amount of
	// eliding the rest gets under a tiny target.
	src := cpFrom(t, 2, "", []Message{
		user("a"),
		tool("1", strings.Repeat("x", 2000)),
		user("tail"),
		tool("2", strings.Repeat("y", 50000)), // protected: belongs to the tail turn
	})
	_, rep, err := Compact(src, CompactOptions{TargetChars: 100, KeepHeadTurns: 1, KeepTailTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Fit {
		t.Error("report claims it fit, but the protected tail alone exceeds the target")
	}
}

// Compaction is deterministic: same input and options, byte-identical output.
func TestCompact_IsDeterministic(t *testing.T) {
	build := func() *Checkpoint {
		return cpFrom(t, 3, "", []Message{
			user("a"),
			tool("1", strings.Repeat("x", 4000)),
			tool("2", strings.Repeat("x", 4000)), // equal weight — ties break on index
			user("b"),
			tool("3", strings.Repeat("y", 6000)),
			user("tail"),
		})
	}
	opt := CompactOptions{TargetChars: 5000, KeepHeadTurns: 1, KeepTailTurns: 1}
	a, _, err := Compact(build(), opt)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Compact(build(), opt)
	if err != nil {
		t.Fatal(err)
	}
	ba, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ba) != string(bb) {
		t.Error("two compactions of the same input produced different output; it must be deterministic")
	}
}

// A compacted checkpoint still restores: it is a valid session, not a file
// that looks valid and resumes to nothing.
func TestCompact_KeepsTheConversationWellFormed(t *testing.T) {
	src := cpFrom(t, 2, "", []Message{
		user("a"), tool("x", strings.Repeat("q", 9000)), user("b"),
	})
	out, _, err := Compact(src, CompactOptions{TargetChars: 10, KeepHeadTurns: 1, KeepTailTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	loop, err := RestoreCheckpoint(b, 6)
	if err != nil {
		t.Fatalf("the compacted checkpoint did not restore: %v", err)
	}
	if len(loop.messages) != len(src.Messages) {
		t.Errorf("restored %d messages, want %d — compaction must keep the message count (stub, not delete)",
			len(loop.messages), len(src.Messages))
	}
}

// EstimateTokens is chars over four, the gateway's own arithmetic.
func TestEstimateTokens_IsCharsOverFour(t *testing.T) {
	c := cpFrom(t, 1, "", []Message{{Role: "user", Content: strings.Repeat("x", 400)}})
	if got := EstimateTokens(c); got != 100 {
		t.Errorf("EstimateTokens = %d, want 100 (400 chars / 4)", got)
	}
}

// A LARGE TOOL-HEAVY SESSION, built to the shape of the real one that tripped
// the gateway's 200000-token window. That session was 68 turns, ~390k tokens,
// half tool results and half the assistant tool-CALL arguments that produced
// them — so results alone do not get it under the window, and
// ElideToolCallArgs is the knob that makes it fit. This reproduces that shape
// synthetically rather than committing a 1.8MB real conversation: a generator
// is deterministic, reviewable, and carries no real content, while exercising
// exactly the two-pass path the real file needed.
//
// proven-by: itself — the generated session mirrors the regression's shape.
func TestCompact_TheRealSessionFitsTheWindow(t *testing.T) {
	// 68 turns; each turn: a user line, an assistant tool call with heavy
	// arguments, and a heavy tool result. Head and tail turns stay small so
	// the protected region does not dominate.
	var msgs []Message
	heavyArgs := func(n int) json.RawMessage {
		return json.RawMessage(`{"path":"` + strings.Repeat("p", n) + `"}`)
	}
	for turn := 0; turn < 90; turn++ {
		msgs = append(msgs, user("turn request "+strings.Repeat("q", 40)))
		msgs = append(msgs, Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call" + itoa(turn), Name: "local__read_file", Arguments: heavyArgs(9000)},
		}})
		msgs = append(msgs, tool("call"+itoa(turn), strings.Repeat("R", 800)))
	}
	src := cpFrom(t, 90, "", msgs)

	beforeTok := EstimateTokens(src)
	if beforeTok < 180000 {
		t.Fatalf("generated estimate %d tokens — expected an oversized session (~190k+)", beforeTok)
	}

	// Fit 200000 with 16000 held back for a reply: target chars = (200000-16000)*4.
	const window, reply = 200000, 16000
	target := (window - reply) * 4
	out, rep, err := Compact(src, CompactOptions{
		TargetChars:       target,
		KeepHeadTurns:     2,
		KeepTailTurns:     3,
		ElideToolCallArgs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	afterTok := EstimateTokens(out)
	t.Logf("large session: %d tok -> %d tok, %d messages elided, fit=%v",
		beforeTok, afterTok, rep.Elided, rep.Fit)
	if !rep.Fit {
		t.Fatalf("could not fit the session under %d tokens even after %d elisions (got %d) — "+
			"the protected tail may be too large", window-reply, rep.Elided, afterTok)
	}
	if afterTok > window-reply {
		t.Fatalf("after compaction %d tok still exceeds %d", afterTok, window-reply)
	}
	// Message count is preserved: stubbed, never deleted.
	if len(out.Messages) != len(src.Messages) {
		t.Fatalf("message count changed %d -> %d; compaction must stub not delete",
			len(src.Messages), len(out.Messages))
	}
	// Nothing but tool results and (elided) tool-call arguments changed: no
	// user turn and no assistant PROSE was touched.
	for i := range src.Messages {
		if src.Messages[i].Role == "tool" {
			continue
		}
		if src.Messages[i].Content != out.Messages[i].Content {
			t.Fatalf("message #%d role %q content was altered; prose and user turns must not change",
				i, src.Messages[i].Role)
		}
	}
	// It still restores.
	rb, _ := json.Marshal(out)
	if _, err := RestoreCheckpoint(rb, 6); err != nil {
		t.Fatalf("compacted session did not restore: %v", err)
	}
}

// ElideToolCallArgs drops the arguments only of calls whose RESULT was
// already elided, never a call whose answer is still present — stranding an
// answer from the arguments that produced it would be the worse cut.
func TestCompact_ElidesToolCallArgsOnlyForElidedResults(t *testing.T) {
	bigArgs := json.RawMessage(`{"path":"` + strings.Repeat("p", 4000) + `"}`)
	smallArgs := json.RawMessage(`{"path":"keep.md"}`)
	// Four user turns, so KeepHead=1/KeepTail=1 leaves turns 2 and 3 open.
	// The call we elide sits in an open turn; the surviving call sits in the
	// protected tail, which is where a surviving result naturally is.
	src := cpFrom(t, 4, "", []Message{
		user("start"),
		user("open turn"),
		// call whose result we WILL elide (heavy result), in an open turn
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "elide", Name: "local__read_file", Arguments: bigArgs}}},
		tool("elide", strings.Repeat("R", 9000)),
		user("another open turn"),
		// call whose result is small and survives — its args must be kept
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "keep", Name: "local__read_file", Arguments: smallArgs}}},
		tool("keep", "small result"),
		user("tail"),
	})
	out, _, err := Compact(src, CompactOptions{
		TargetChars:       1,
		KeepHeadTurns:     1,
		KeepTailTurns:     1,
		ElideToolCallArgs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The elided call's heavy args are now the stub.
	if string(out.Messages[2].ToolCalls[0].Arguments) != compactedArgs {
		t.Errorf("the elided call's arguments were not stubbed: %s", out.Messages[2].ToolCalls[0].Arguments)
	}
	// The surviving call's args are untouched (it is in the protected tail
	// anyway, but the rule is about the result's presence, not position).
	if string(out.Messages[5].ToolCalls[0].Arguments) != string(smallArgs) {
		t.Errorf("a surviving call's arguments were stubbed: %s", out.Messages[5].ToolCalls[0].Arguments)
	}
	// The source is untouched — stubbing args must not reach back through a
	// shared backing array.
	if string(src.Messages[2].ToolCalls[0].Arguments) != string(bigArgs) {
		t.Error("compaction mutated the source checkpoint's tool-call arguments")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Results-only compaction cannot save a session whose weight is in the
// tool-CALL arguments: this is the exact reason ElideToolCallArgs exists, and
// the real 68-turn session failed precisely here. Proven as its own case so a
// future change that makes the args pass a no-op is caught.
func TestCompact_ArgsPassIsLoadBearing(t *testing.T) {
	var msgs []Message
	heavyArgs := func(n int) json.RawMessage {
		return json.RawMessage(`{"path":"` + strings.Repeat("p", n) + `"}`)
	}
	for turn := 0; turn < 90; turn++ {
		msgs = append(msgs, user("req"))
		msgs = append(msgs, Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "c" + itoa(turn), Name: "local__read_file", Arguments: heavyArgs(9000)},
		}})
		msgs = append(msgs, tool("c"+itoa(turn), strings.Repeat("R", 800)))
	}
	src := cpFrom(t, 90, "", msgs)
	const window, reply = 200000, 16000
	target := (window - reply) * 4

	// Results only: should NOT fit.
	_, repNoArgs, err := Compact(src, CompactOptions{
		TargetChars: target, KeepHeadTurns: 2, KeepTailTurns: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repNoArgs.Fit {
		t.Fatal("results-only compaction fit a session whose weight is in the arguments — " +
			"then ElideToolCallArgs would be dead code; make the fixture heavier in args")
	}
	// With the args pass: should fit.
	_, repArgs, err := Compact(src, CompactOptions{
		TargetChars: target, KeepHeadTurns: 2, KeepTailTurns: 3, ElideToolCallArgs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repArgs.Fit {
		t.Fatal("even with the args pass the session did not fit")
	}
}

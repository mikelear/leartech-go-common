package agentloop

import (
	"encoding/json"
	"fmt"
	"sort"
)

// CompactOptions controls how Compact shrinks a conversation.
//
// EVERY KNOB HAS A SAFE ZERO so a caller can ask for "compact to fit" with
// one field. The zero value compacts nothing, because a Compact that threw
// work away by default would be a footgun on a type whose whole job is to be
// trusted with a conversation.
type CompactOptions struct {
	// TargetChars is the conversation size to get under, measured the way
	// the gateway measures a prompt: the sum of message content plus the
	// JSON of any tool calls. Compact elides the heaviest eligible messages
	// until the estimate is at or below this, or until nothing eligible is
	// left. Zero means "do not compact by size".
	//
	// The gateway's own gate is estTokens = chars/4 + max_tokens against the
	// model's context window, so a caller that wants to fit a 200000-token
	// window with room for a reply passes roughly (200000-reply)*4 here.
	TargetChars int

	// KeepHeadTurns is how many of the EARLIEST user turns to protect from
	// elision, counted in user messages. The opening of a session carries
	// the task and the ground rules a later turn still refers back to, so
	// cutting it is the one elision a reader is most likely to regret. One is
	// a sensible floor; zero protects none.
	// proven-by: TestCompact_ProtectsTheHeadAndTailTurns
	KeepHeadTurns int

	// KeepTailTurns is how many of the MOST RECENT user turns to protect,
	// counted in user messages. The tail is what the next turn builds on.
	// One is the floor; zero protects none.
	// proven-by: TestCompact_ProtectsTheHeadAndTailTurns
	KeepTailTurns int

	// ElideToolCallArgs also stubs the ARGUMENTS of an assistant tool call
	// once that call's result has been elided, keeping the call's id and
	// name so the turn stays well-formed. This matters because in a long
	// tool-heavy session the accumulated tool-call arguments can be as heavy
	// as the results themselves — in the real 68-turn session that tripped
	// the window, assistant tool_calls were 48% of the bytes. Eliding the
	// result but keeping the 40KB arguments that produced it leaves half the
	// weight behind. Off by default: it is the more aggressive cut, and a
	// caller that only needs a little room should not pay it.
	ElideToolCallArgs bool

	// Stub is the text a compacted tool result is replaced with. The elided
	// length is appended, so a reader sees that bytes were removed rather
	// than that a tool returned nothing. Empty uses a default.
	Stub string
}

// CompactReport says what Compact did, so a caller can show it and a test
// can assert on it without re-deriving the arithmetic.
type CompactReport struct {
	// BeforeChars and AfterChars are the conversation estimate either side,
	// measured the gateway's way. AfterChars <= BeforeChars always.
	BeforeChars int
	AfterChars  int

	// Elided is how many messages had their body (tool result, or tool-call
	// arguments) replaced with the stub.
	Elided int

	// Fit is whether AfterChars reached TargetChars. False with a non-zero
	// Elided means everything eligible was compacted and the conversation is
	// still over target — the caller's signal that the protected tail alone
	// exceeds the target.
	// proven-by: TestCompact_ReportsWhenItCannotFit
	Fit bool
}

const (
	defaultCompactStub = "[compacted: tool output elided to fit the context window]"
	compactedArgs      = `{"compacted":"arguments elided to fit the context window"}`
)

// Compact returns a conversation with the heaviest re-derivable material —
// tool results, and optionally the tool-call arguments that produced them —
// replaced by a short stub, re-chained onto the checkpoint it compacted.
//
// WHY THESE TWO KINDS AND NOTHING ELSE. A conversation's weight is three
// things: the user's turns, the assistant's prose and tool CALLS, and the
// tool RESULTS those calls returned. The user's turns are the task and must
// left alone. The assistant's PROSE is the reasoning the next turn relies
// on, so it is left alone too. The two kinds that are both
// heavy and re-derivable are the tool RESULTS (a file the agent read, a
// listing it took — it can read it again) and, once a result is gone, the
// ARGUMENTS of the call that asked for it (which were only ever a means to
// that result). So Compact elides tool results, and — with
// ElideToolCallArgs — the arguments of calls whose result it already elided,
// and nothing else. This is the deterministic, explainable cut: no model
// decides what matters, the structure does.
//
// WHY A STUB RATHER THAN DELETION. Deleting a tool message orphans the
// assistant tool_call that asked for it — the far end sees a call with no
// result and some providers reject the turn. The stub keeps the message in
// place, keyed to its call, carrying a note that its body was removed, so
// the conversation stays well-formed and the agent can see a gap was made on
// purpose rather than infer the tool failed. The same reasoning keeps a
// tool call's id and name when its arguments are stubbed.
//
// WHY RE-CHAINED. The returned checkpoint's PrevHash is the source
// checkpoint's Hash, exactly as a resumed session chains onto the file it
// resumed. A compacted checkpoint is a deliberate edit of history, and the
// chain records it as one link rather than hiding it: the next checkpoint
// the resumed session writes extends this, and anyone comparing hashes sees
// the compaction happened here, by this process, on purpose.
//
// It is DETERMINISTIC: the same input and options produce the same output,
// byte for byte, so it is unit-testable without a model and reproducible
// when something looks wrong.
//
// proven-by: TestCompact_ElidesTheHeaviestToolResultsFirst
// proven-by: TestCompact_NeverTouchesUserOrAssistantMessages
// proven-by: TestCompact_ProtectsTheHeadAndTailTurns
// proven-by: TestCompact_ReChainsOntoTheSourceCheckpoint
// proven-by: TestCompact_StopsOnceUnderTarget
// proven-by: TestCompact_ReportsWhenItCannotFit
// proven-by: TestCompact_IsDeterministic
// proven-by: TestCompact_KeepsTheConversationWellFormed
// proven-by: TestCompact_ElidesToolCallArgsOnlyForElidedResults
// proven-by: TestCompact_ArgsPassIsLoadBearing
// proven-by: TestCompact_TheRealSessionFitsTheWindow
func Compact(src *Checkpoint, opt CompactOptions) (*Checkpoint, CompactReport, error) {
	if src == nil {
		return nil, CompactReport{}, fmt.Errorf("compact: no checkpoint to compact")
	}
	if src.Version != CheckpointVersion {
		return nil, CompactReport{}, fmt.Errorf("compact: version %d is not %d — this file was "+
			"written by a different release; refuse rather than guess", src.Version, CheckpointVersion)
	}
	if len(src.Messages) == 0 {
		return nil, CompactReport{}, fmt.Errorf("compact: no messages — an empty conversation " +
			"compacted as a success would be a silent reset")
	}

	stub := opt.Stub
	if stub == "" {
		stub = defaultCompactStub
	}

	// Work on a copy so the source checkpoint is left as the caller's
	// original. The ToolCalls slice is copied too, so stubbing an argument
	// does not reach back through a shared backing array into the source.
	// proven-by: TestCompact_ElidesToolCallArgsOnlyForElidedResults
	msgs := make([]Message, len(src.Messages))
	copy(msgs, src.Messages)

	before := conversationChars(msgs)
	report := CompactReport{BeforeChars: before, AfterChars: before}

	if opt.TargetChars <= 0 || before <= opt.TargetChars {
		// Nothing to do, but still return a valid re-chained checkpoint so
		// the caller gets one either way.
		report.Fit = true
		out, err := rechained(src, msgs)
		return out, report, err
	}

	eligible := eligibleToolResults(msgs, opt.KeepHeadTurns, opt.KeepTailTurns)

	// Heaviest first: the point of compaction is to buy the most room for
	// the fewest elisions, so one 46k tool dump goes before fifty small
	// listings. Ties break on index so the result is deterministic.
	sort.SliceStable(eligible, func(i, j int) bool {
		li, lj := messageChars(msgs[eligible[i]]), messageChars(msgs[eligible[j]])
		if li != lj {
			return li > lj
		}
		return eligible[i] < eligible[j]
	})

	cur := before
	// elidedCallIDs records which calls had their RESULT removed, so a later
	// pass may also drop those calls' arguments without ever touching a call
	// whose result is still present.
	elidedCallIDs := map[string]bool{}
	for _, idx := range eligible {
		if cur <= opt.TargetChars {
			break
		}
		cur -= stubResult(&msgs[idx], stub)
		if id := msgs[idx].ToolCallID; id != "" {
			elidedCallIDs[id] = true
		}
		report.Elided++
	}

	// Second pass: drop the arguments of calls whose result we already
	// elided. Only reached when results alone did not get under target and
	// the caller allowed it — the real tool-heavy session needs it, a small
	// trim does not.
	if opt.ElideToolCallArgs && cur > opt.TargetChars {
		for i := range msgs {
			if cur <= opt.TargetChars {
				break
			}
			if msgs[i].Role != "assistant" || len(msgs[i].ToolCalls) == 0 {
				continue
			}
			saved := stubCallArgs(&msgs[i], elidedCallIDs)
			if saved > 0 {
				cur -= saved
				report.Elided++
			}
		}
	}

	report.AfterChars = conversationChars(msgs)
	report.Fit = report.AfterChars <= opt.TargetChars
	out, err := rechained(src, msgs)
	return out, report, err
}

// stubResult replaces a tool result's body with the stub and returns how many
// characters that removed.
func stubResult(m *Message, stub string) int {
	before := messageChars(*m)
	elided := len(m.Content)
	m.Content = fmt.Sprintf("%s (%d chars elided)", stub, elided)
	return before - messageChars(*m)
}

// stubCallArgs replaces the Arguments of this assistant message's tool calls
// whose id is in elided, keeping each call's id and name so the turn stays
// well-formed. Returns the characters removed. A call whose result is still
// present is left alone — its arguments are the only record of what was
// asked, and dropping them while keeping the answer would strand the answer.
func stubCallArgs(m *Message, elided map[string]bool) int {
	saved := 0
	calls := make([]ToolCall, len(m.ToolCalls))
	copy(calls, m.ToolCalls)
	for i := range calls {
		if !elided[calls[i].ID] {
			continue
		}
		if len(calls[i].Arguments) <= len(compactedArgs) {
			continue // already small; stubbing would not help
		}
		saved += len(calls[i].Arguments) - len(compactedArgs)
		calls[i].Arguments = json.RawMessage(compactedArgs)
	}
	if saved > 0 {
		m.ToolCalls = calls
	}
	return saved
}

// eligibleToolResults is the set of message indices Compact may elide: tool
// results, outside the protected head and tail turns, that still carry a
// body worth removing.
//
// TURNS ARE COUNTED IN USER MESSAGES because that is how a person counts a
// conversation — "the first thing I asked", "the last thing I asked" — and
// it is the boundary the checkpoint's own Turn field counts. A tool result
// belongs to the turn of the most recent user message before it.
func eligibleToolResults(msgs []Message, keepHead, keepTail int) []int {
	// Index every user message, so a tool result can be placed in its turn.
	var userAt []int
	for i, m := range msgs {
		if m.Role == "user" {
			userAt = append(userAt, i)
		}
	}
	// The message index at which the protected tail begins. Everything from
	// here on is off limits.
	tailStart := len(msgs)
	if keepTail > 0 && len(userAt) > 0 {
		k := keepTail
		if k > len(userAt) {
			k = len(userAt)
		}
		tailStart = userAt[len(userAt)-k]
	}
	// The message index at which the protected head ends. Everything before
	// here is off limits. headEnd is the index of the (keepHead)th user
	// message, so the first keepHead turns and their results are protected.
	headEnd := 0
	if keepHead > 0 && len(userAt) > 0 {
		k := keepHead
		if k >= len(userAt) {
			// Protecting every head turn protects everything up to the tail.
			headEnd = tailStart
		} else {
			headEnd = userAt[k]
		}
	}

	var out []int
	for i, m := range msgs {
		if i < headEnd || i >= tailStart {
			continue
		}
		if m.Role != "tool" {
			continue
		}
		if len(m.Content) == 0 {
			continue // nothing to elide
		}
		out = append(out, i)
	}
	return out
}

// rechained builds a checkpoint from the compacted messages whose PrevHash
// links it to the source — the same move a resumed session makes onto the
// file it resumed.
func rechained(src *Checkpoint, msgs []Message) (*Checkpoint, error) {
	c := &Checkpoint{
		Version:  CheckpointVersion,
		Turn:     src.Turn,
		PrevHash: src.Hash,
		Messages: msgs,
	}
	c.Hash = c.computeHash()
	// A compacted conversation is re-validated through RestoreCheckpoint so a
	// bug that emptied it is caught here rather than written as a
	// valid-looking file that resumes to nothing.
	// proven-by: TestCompact_KeepsTheConversationWellFormed
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("compact: marshalling the compacted checkpoint: %w", err)
	}
	if _, err := RestoreCheckpoint(b, 0); err != nil {
		return nil, fmt.Errorf("compact: the compacted checkpoint would not restore: %w", err)
	}
	return c, nil
}

// messageChars is one message's weight, measured the way the gateway
// measures a prompt: content plus the JSON of any tool calls. A tool call
// with no arguments still costs its name and id on the wire, so it is
// counted through its parts rather than guessed at.
func messageChars(m Message) int {
	n := len(m.Content) + len(m.ToolCallID)
	for _, tc := range m.ToolCalls {
		n += len(tc.ID) + len(tc.Name) + len(tc.Arguments)
	}
	return n
}

// conversationChars is the whole conversation's estimate, the number the
// gateway's context-window gate divides by four.
func conversationChars(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += messageChars(m)
	}
	return n
}

// EstimateTokens is the gateway's own text estimate: characters over four.
// Exported so a caller sizing a compaction uses the same arithmetic the
// gateway enforces with, rather than a second guess that disagrees at the
// boundary.
//
// proven-by: TestEstimateTokens_IsCharsOverFour
func EstimateTokens(c *Checkpoint) int {
	if c == nil {
		return 0
	}
	return conversationChars(c.Messages) / 4
}

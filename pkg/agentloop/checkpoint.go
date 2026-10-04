package agentloop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// CheckpointVersion is the on-disk shape of a checkpoint. A future change
// that a resumed reader must notice bumps this; a resume that does not
// recognise the version refuses rather than guessing.
const CheckpointVersion = 1

// Checkpoint is the loop's conversation at a boundary where it is safe to
// stop and later continue: every turn ended, no tool call outstanding.
//
// WHY A NEW TYPE RATHER THAN THE TRANSCRIPT. The transcript records kinds
// and LENGTHS of what was shown, deliberately, so no test asserts on model
// prose — and deliberately un-restoreable: there is no state machine
// described, only actions. A checkpoint is the opposite trade: it carries
// the messages, because its whole job is to be loaded back. It is written
// by a different caller (the session owner, not the test harness) to a
// different place (a 0600 file the caller owns), so the transcript keeps
// its properties untouched.
//
// THE TURN BOUNDARY, AND ONLY THE TURN BOUNDARY. A checkpoint taken while
// the loop is Running or Asking would resume a half-executed chain of tool
// calls, or answer a question nobody re-asked. Save refuses; the caller
// retries at the next boundary. // proven-by: TestCheckpoint_RefusesAMidTurnState
//
// proven-by: TestCheckpoint_ATurnBoundaryRoundTrips
// proven-by: TestCheckpoint_RefusesAMidTurnState
type Checkpoint struct {
	Version int `json:"version"`

	// Turn is how many user inputs the conversation has served. It is the
	// checkpoint's place in the chain: consecutive checkpoints compare turn
	// numbers, and a gap means a turn was lost between two writes.
	Turn int `json:"turn"`

	// PrevHash is Hash() of the checkpoint before this one; empty on the
	// first. Hash is of this checkpoint's messages, so the chain is
	// append-only by construction — an edit to history changes every hash
	// after it.
	PrevHash string `json:"prev_hash"`

	// Hash is computed over (turn, prevHash, messages) at construction, so
	// a copy of the file cannot claim to extend a chain it does not.
	Hash string `json:"hash"`

	// Messages is the whole conversation, including the system message the
	// loop was constructed with, in order.
	Messages []Message `json:"messages"`
}

// Checkpoint returns the conversation as it stands, or an error when the
// loop is mid-turn.
//
// The loop's messages are copied — conversation() already returns a copy —
// so a checkpoint is a snapshot rather than a live handle; the caller may
// hold it, marshal it and write it without racing the next turn.
//
// turn counts the inputs the caller has driven through Step, which the loop
// itself does not track: it is passed here because the checkpoint chain
// needs it and the CALLER is the one who knows where one input ended and
// the next began. (The runner reads one line per input; the loop sees only
// events.)
//
// proven-by: TestCheckpoint_ATurnBoundaryRoundTrips
// proven-by: TestCheckpoint_RefusesAMidTurnState
func (l *Loop) Checkpoint(turn int, prevHash string) (*Checkpoint, error) {
	switch l.state {
	case Running, Asking, Awaiting:
		return nil, fmt.Errorf(
			"checkpoint: the loop is %s, not idle — a checkpoint mid-turn would "+
				"resume a half-finished one; retry at the next turn boundary", l.state)
	}
	c := &Checkpoint{
		Version:  CheckpointVersion,
		Turn:     turn,
		PrevHash: prevHash,
		Messages: append([]Message(nil), l.messages...),
	}
	c.Hash = c.computeHash()
	return c, nil
}

// RestoreCheckpoint returns a loop holding the checkpoint's conversation,
// ready for the next input.
//
// A NEW LOOP RATHER THAN A MUTATION of the caller's, deliberately: the
// runner creates its loop once and drives everything through Step; a
// second way to change loop state would be a second place for the state to
// go wrong. This is the same construction New performs, plus messages.
//
// The version is checked BEFORE anything else, and a message the caller
// cannot act on says which version was found and which was expected — a
// resume that read a future file and produced an empty conversation would
// look like a success.
//
// The CHAIN is not verified here. prev_hash links two checkpoints the
// caller holds; this constructor's job is one checkpoint, and the caller —
// who has both files — is the one who can compare. (See CheckpointChain.)
//
// maxTools is the caller's to choose again: the budget that ran out on the
// session being resumed is not a value to inherit silently.
//
// proven-by: TestRestoreCheckpoint_RebuildsTheConversation
// proven-by: TestRestoreCheckpoint_ContinuesAccumulating
// proven-by: TestRestoreCheckpoint_RefusesAnUnknownVersion
// proven-by: TestRestoreCheckpoint_AnEmptyCheckpointIsAnError
func RestoreCheckpoint(b []byte, maxTools int) (*Loop, error) {
	var c Checkpoint
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("checkpoint: not valid JSON: %w", err)
	}
	if c.Version != CheckpointVersion {
		return nil, fmt.Errorf("checkpoint: version %d is not %d — this file was "+
			"written by a different release; refuse rather than guess", c.Version, CheckpointVersion)
	}
	if len(c.Messages) == 0 {
		return nil, fmt.Errorf("checkpoint: no messages — an empty conversation " +
			"restored as a success would be a silent reset")
	}
	l := &Loop{state: Idle, maxTools: maxTools, budget: maxTools}
	l.messages = append(l.messages, c.Messages...)
	return l, nil
}

// CheckpointChainedFrom returns a checkpoint whose PrevHash is the
// checkpoint the loop was restored from, so a resumed session chains onto
// the one it resumed.
//
// The loop does not know its own history's hash (New does not take one), so
// this asks the caller to supply the checkpoint that was restored; the
// caller holds it because it read the file. The returned checkpoint's
// PrevHash is that file's Hash, which makes the resumed chain continuous
// across the restart by construction.
//
// proven-by: TestCheckpoint_ARestoredSessionChainsOntoTheFileItResumed
func (l *Loop) CheckpointChainedFrom(restored *Checkpoint, turn int) (*Checkpoint, error) {
	if restored == nil {
		return l.Checkpoint(turn, "")
	}
	return l.Checkpoint(turn, restored.Hash)
}

// computeHash covers everything that makes this checkpoint what it is.
//
// Messages are marshalled deterministically (Go's json marshals struct
// fields in declaration order, and this type has no maps), so the same
// conversation always hashes the same.
func (c *Checkpoint) computeHash() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", c.Turn, c.PrevHash, c.messagesKey())))
	return hex.EncodeToString(sum[:])
}

// messagesKey renders the messages in a fixed, unambiguous form for hashing.
func (c *Checkpoint) messagesKey() string {
	var b strings.Builder
	for _, m := range c.Messages {
		fmt.Fprintf(&b, "%s\x1f%s\x1f%s\x1f[", m.Role, m.Content, m.ToolCallID)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "%s\x1f%s\x1e", tc.ID, tc.Name)
		}
		b.WriteString("]\x1e")
	}
	return b.String()
}

package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/aigateway"
)

// scriptLines feeds fixed lines, then EOF — the Lines port the loop already
// knows, so the checkpoint tests run the real Run/RunRestored path.
type scriptLines struct {
	lines []string
	i     int
}

func (s *scriptLines) ReadLine() (string, error) {
	if s.i >= len(s.lines) {
		return "", io.EOF
	}
	l := s.lines[s.i]
	s.i++
	return l, nil
}

func (s *scriptLines) Close() error { return nil }

// answeringStreamer replies to every send with one text chunk and a stop,
// recording what it was sent so a test can assert on the conversation the
// model actually saw.
type answeringStreamer struct {
	sent [][]aigateway.ChatRequestMessage
}

func (s *answeringStreamer) ChatStream(_ context.Context, req aigateway.ChatRequest) (<-chan aigateway.StreamChunk, error) {
	s.sent = append(s.sent, req.Messages)
	ch := make(chan aigateway.StreamChunk, 2)
	ch <- aigateway.StreamChunk{Kind: aigateway.StreamText, Text: "ok"}
	ch <- aigateway.StreamChunk{Kind: aigateway.StreamDone, Finish: "stop"}
	close(ch)
	return ch, nil
}

// A run with OnBoundary writes one checkpoint per input, chained.
func TestRun_WritesABoundaryCheckpoint(t *testing.T) {
	st := &answeringStreamer{}
	var cks [][]byte
	r := &Runner{
		Model: "m", Client: st, Out: discard{},
		OnBoundary: func(b []byte) error { cks = append(cks, b); return nil },
	}
	in := &scriptLines{lines: []string{"one", "two"}}
	if err := r.Run(context.Background(), in, "sys", 4); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(cks) != 2 {
		t.Fatalf("checkpoints = %d, want 2 (one per input)", len(cks))
	}
	var first, second Checkpoint
	if err := json.Unmarshal(cks[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(cks[1], &second); err != nil {
		t.Fatal(err)
	}
	if first.Turn != 1 || second.Turn != 2 {
		t.Errorf("turns = %d,%d; want 1,2 — turns continue counting up", first.Turn, second.Turn)
	}
	if second.PrevHash != first.Hash {
		t.Errorf("second prev_hash = %s, want the first checkpoint's %s — the chain must link", second.PrevHash, first.Hash)
	}
}

// OnBoundary == nil costs nothing and fails nothing: the ordinary run.
func TestRun_NoBoundaryCallbackIsFine(t *testing.T) {
	st := &answeringStreamer{}
	r := &Runner{Model: "m", Client: st, Out: discard{}}
	if err := r.Run(context.Background(), &scriptLines{lines: []string{"hi"}}, "sys", 4); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// A failing OnBoundary fails the run: a chain with a hole in it resumes
// somewhere the operator was not told about.
func TestRun_ACheckpointWriteFailureFailsTheRun(t *testing.T) {
	st := &answeringStreamer{}
	r := &Runner{
		Model: "m", Client: st, Out: discard{},
		OnBoundary: func(b []byte) error { return errors.New("disk full") },
	}
	err := r.Run(context.Background(), &scriptLines{lines: []string{"hi"}}, "sys", 4)
	if err == nil {
		t.Fatal("a checkpoint write failure was swallowed; the chain would have a hole")
	}
}

// RunRestored continues the conversation: the model is sent the restored
// history plus the new input, not the new input alone.
func TestRunRestored_ContinuesTheConversationFromTheCheckpoint(t *testing.T) {
	first := &answeringStreamer{}
	r1 := &Runner{Model: "m", Client: first, Out: discard{}, OnBoundary: func(b []byte) error { return nil }}
	var ck []byte
	r1.OnBoundary = func(b []byte) error { ck = b; return nil }
	if err := r1.Run(context.Background(), &scriptLines{lines: []string{"original question"}}, "sys", 4); err != nil {
		t.Fatal(err)
	}
	loop, err := RestoreCheckpoint(ck, 4)
	if err != nil {
		t.Fatal(err)
	}
	var restored Checkpoint
	_ = json.Unmarshal(ck, &restored)

	st := &answeringStreamer{}
	r2 := &Runner{Model: "m", Client: st, Out: discard{}}
	if err := r2.RunRestored(context.Background(), loop, &restored, &scriptLines{lines: []string{"follow up"}}, 4); err != nil {
		t.Fatal(err)
	}
	if len(st.sent) == 0 {
		t.Fatal("the resumed run sent nothing")
	}
	sawOld, sawNew := false, false
	for _, m := range st.sent[0] {
		if m.Content == "original question" {
			sawOld = true
		}
		for _, blk := range m.Blocks {
			if blk.Text == "follow up" {
				sawNew = true
			}
		}
		if m.Content == "follow up" {
			sawNew = true
		}
	}
	if !sawOld || !sawNew {
		t.Errorf("the model was sent %v; want the restored history AND the new input", st.sent[0])
	}
}

// The resumed run's first checkpoint chains onto the file it resumed.
func TestRunRestored_ChainsOntoTheCheckpointItResumed(t *testing.T) {
	var firstCK []byte
	r1 := &Runner{Model: "m", Client: &answeringStreamer{}, Out: discard{},
		OnBoundary: func(b []byte) error { firstCK = b; return nil }}
	if err := r1.Run(context.Background(), &scriptLines{lines: []string{"one"}}, "sys", 4); err != nil {
		t.Fatal(err)
	}
	loop, _ := RestoreCheckpoint(firstCK, 4)
	var restored Checkpoint
	_ = json.Unmarshal(firstCK, &restored)

	var secondCK []byte
	r2 := &Runner{Model: "m", Client: &answeringStreamer{}, Out: discard{},
		OnBoundary: func(b []byte) error { secondCK = b; return nil }}
	if err := r2.RunRestored(context.Background(), loop, &restored, &scriptLines{lines: []string{"two"}}, 4); err != nil {
		t.Fatal(err)
	}
	var second Checkpoint
	_ = json.Unmarshal(secondCK, &second)
	if second.PrevHash != restored.Hash {
		t.Errorf("resumed prev_hash = %s, want the resumed file's %s", second.PrevHash, restored.Hash)
	}
	if second.Turn != restored.Turn+1 {
		t.Errorf("resumed turn = %d, want %d — turns continue counting up", second.Turn, restored.Turn+1)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

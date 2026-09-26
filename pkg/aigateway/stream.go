package aigateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// StreamKind is what a chunk carries. It mirrors the gateway's adapter.Chunk
// discriminator rather than inferring shape from which fields are non-nil,
// which is what the gateway did before ai-gateway#81 and is how a streamed
// tool call reached clients as an empty delta.
type StreamKind int

// The kinds a streamed turn can carry.
const (
	StreamText      StreamKind = iota // Text is a delta of the answer
	StreamReasoning                   // Text is a delta of the model's thinking
	StreamToolCall                    // ToolCall is complete and ready to run
	StreamDone                        // Finish is set
	StreamError                       // Err is set
	StreamUsage                       // Usage is set
)

// ToolCall is one complete call; the gateway reassembles fragments.
// proven-by: TestChatStream_AnIncompleteToolCallIsRefusedNotExecuted
//
// Arguments stays json.RawMessage: the envelope is ours, the payload is the
// model's.
// proven-by: TestChatStream_DeliversEachKindFromTheRealFrameShapes
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// StreamErr is a terminal error carried IN the stream rather than as a
// transport failure.
//
// Retryable travels because a loop has to decide whether to try again.
// proven-by: TestChatStream_AnErrorFrameIsDeliveredNotSwallowed
type StreamErr struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *StreamErr) Error() string {
	return fmt.Sprintf("gateway stream: %s: %s", e.Code, e.Message)
}

// StreamChunk is one event from a streamed completion.
type StreamChunk struct {
	Kind     StreamKind
	Text     string
	ToolCall *ToolCall
	Finish   string
	Err      *StreamErr

	// Usage arrives on its own frame AFTER the finish_reason, so a reader
	// stopping at StreamDone misses it. // proven-by: TestChatStream_UsageArrivesAfterTheFinishReason
	Usage *ChatUsage
}

// sseFrame is the gateway's chunk envelope.
//
// source: leartech-ai-gateway internal/api/handlers.go, the c.Stream emit
// closure — "chat.completion.chunk" with one choice, plus a bare {"error":…}
// frame and a literal [DONE] sentinel.
type sseFrame struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`

	// Usage rides an otherwise EMPTY frame — no choices at all — which is
	// why it has to be read before the empty-choices skip below.
	//
	// source: leartech-ai-gateway internal/api/handlers.go, the streamChat
	// emit closure: `emitUsage := func(u Usage) { frame([]map[string]any{}, u) }`
	// builds the envelope with an empty choices slice and sets payload["usage"].
	// Sent only when the request asked, via stream_options.include_usage.
	Usage *ChatUsage `json:"usage"`
}

// ChatStream streams a completion, returning a channel closed when the stream
// ends for any reason.
//
// proven-by: TestChatStream_TheChannelAlwaysCloses — transport failure
// arrives as a StreamError chunk and then the channel closes, which keeps
// "ended" and "ended badly" distinguishable.
//
// proven-by: TestChatStream_DeliversEachKindFromTheRealFrameShapes
// proven-by: TestChatStream_AnErrorFrameIsDeliveredNotSwallowed
// proven-by: TestChatStream_ATruncatedStreamIsAnErrorNotACleanEnd
// proven-by: TestChatStream_TheChannelAlwaysCloses
func (c *Client) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	body, err := json.Marshal(streamRequest{
		ChatRequest:   req,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.base, "/")+"/v1/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	c.stamp(httpReq.Header)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Scrubbed, as the non-streaming path does. A transport error can
		// quote the request, and the request carries the key.
		return nil, fmt.Errorf("gateway: POST /v1/chat/completions (stream): %w", c.scrubErr(err))
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, c.statusError(http.MethodPost, "/v1/chat/completions (stream)",
			resp.StatusCode, raw)
	}

	out := make(chan StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		scanSSE(ctx, bufio.NewScanner(resp.Body), out)
	}()
	return out, nil
}

// streamRequest adds stream:true without putting it on ChatRequest, where it
// would be sent as false on every non-streaming call and read as a deliberate
// opt-out.
type streamRequest struct {
	ChatRequest
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

// streamOptions asks the gateway for the trailing usage frame.
//
// WITHOUT THIS THE SHELL IS BLIND TO WHAT A TURN COST. The non-streaming
// Chat path gets usage in the response body for free; a stream gets it only
// on request, so the streaming client saw no token counts, no cache hits
// and no cost while the one-shot path reported all three.
//
// source: leartech-ai-gateway internal/api/types.go StreamOptions
// (`include_usage`), consumed at handlers.go where streamChat is passed
// `req.StreamOptions != nil && req.StreamOptions.IncludeUsage`.
//
// proven-by: TestChatStream_AsksForUsage
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// scanSSE turns frames into chunks. Separate from the transport so the frame
// grammar can be tested without a server.
func scanSSE(ctx context.Context, sc *bufio.Scanner, out chan<- StreamChunk) {
	sawDone := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			sawDone = true
			break
		}

		var f sseFrame
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			send(ctx, out, StreamChunk{Kind: StreamError, Err: &StreamErr{
				Code:    "malformed_frame",
				Message: fmt.Sprintf("the gateway sent a frame this client cannot parse: %v", err),
			}})
			return
		}
		if f.Error != nil {
			send(ctx, out, StreamChunk{Kind: StreamError, Err: &StreamErr{
				Code: f.Error.Code, Message: f.Error.Message, Retryable: f.Error.Retryable,
			}})
			return
		}
		// BEFORE the empty-choices skip, because the usage frame has no
		// choices and would otherwise be discarded here.
		// proven-by: TestChatStream_UsageArrivesAfterTheFinishReason
		if f.Usage != nil {
			if !send(ctx, out, StreamChunk{Kind: StreamUsage, Usage: f.Usage}) {
				return
			}
		}
		if len(f.Choices) == 0 {
			continue
		}
		if !emitDelta(ctx, out, f) {
			return
		}
	}

	if err := sc.Err(); err != nil {
		send(ctx, out, StreamChunk{Kind: StreamError, Err: &StreamErr{
			Code: "stream_broken", Message: err.Error(), Retryable: true,
		}})
		return
	}
	if !sawDone {
		// A stream that stops without [DONE] is a truncation.
		// proven-by: TestChatStream_ATruncatedStreamIsAnErrorNotACleanEnd
		send(ctx, out, StreamChunk{Kind: StreamError, Err: &StreamErr{
			Code:      "stream_truncated",
			Message:   "the stream ended without [DONE]; the turn is incomplete",
			Retryable: true,
		}})
	}
}

func emitDelta(ctx context.Context, out chan<- StreamChunk, f sseFrame) bool {
	d := f.Choices[0].Delta
	for _, tc := range d.ToolCalls {
		// proven-by: TestChatStream_AnIncompleteToolCallIsRefusedNotExecuted —
		// the gateway reassembles (ai-gateway#81/#83), so a fragment here
		// means that promise broke, and what sits downstream RUNS the call.
		if !json.Valid([]byte(tc.Function.Arguments)) {
			return send(ctx, out, StreamChunk{Kind: StreamError, Err: &StreamErr{
				Code: "tool_call_incomplete",
				Message: fmt.Sprintf("tool %q arrived with arguments that are not "+
					"complete JSON; the gateway reassembles, so this is a lost "+
					"fragment rather than a bad model", tc.Function.Name),
			}})
		}
		if !send(ctx, out, StreamChunk{Kind: StreamToolCall, ToolCall: &ToolCall{
			ID: tc.ID, Name: tc.Function.Name,
			Arguments: json.RawMessage(tc.Function.Arguments),
		}}) {
			return false
		}
	}
	if d.ReasoningContent != "" {
		if !send(ctx, out, StreamChunk{Kind: StreamReasoning, Text: d.ReasoningContent}) {
			return false
		}
	}
	if d.Content != "" {
		if !send(ctx, out, StreamChunk{Kind: StreamText, Text: d.Content}) {
			return false
		}
	}
	if f.Choices[0].FinishReason != "" {
		return send(ctx, out, StreamChunk{Kind: StreamDone, Finish: f.Choices[0].FinishReason})
	}
	return true
}

// send respects cancellation, so a caller that stops reading leaks nothing.
func send(ctx context.Context, out chan<- StreamChunk, c StreamChunk) bool {
	select {
	case out <- c:
		return true
	case <-ctx.Done():
		return false
	}
}

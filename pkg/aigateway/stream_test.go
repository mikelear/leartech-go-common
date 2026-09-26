package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseServer replays frames exactly as the gateway writes them.
//
// The frames below are the gateway's own, not a shape invented here:
// ai-gateway internal/api/handlers.go writes "data: %s\n\n" per chunk and a
// literal "data: [DONE]\n\n" to close, and end2end/06 parses precisely that.
func sseServer(t *testing.T, frames ...string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "t", nil)
}

func drain(t *testing.T, ch <-chan StreamChunk) []StreamChunk {
	t.Helper()
	var got []StreamChunk
	for c := range ch {
		got = append(got, c)
	}
	return got
}

// frame builds a chunk the way the gateway does — json.Marshal of the same
// envelope — so the fixture cannot contain whitespace the real emitter never
// produces. SSE framing is line-based: a payload with an embedded newline is
// not a frame the gateway can send, and hand-written JSON is how that gets in.
func frame(delta map[string]any, finish string) string {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	b, err := json.Marshal(map[string]any{
		"id": "x", "object": "chat.completion.chunk", "created": 1,
		"model": "claude", "choices": []map[string]any{choice},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func errFrame(code, msg string, retryable bool) string {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": code, "message": msg, "retryable": retryable,
	}})
	return string(b)
}

func toolFrame(name, args string) string {
	return frame(map[string]any{"tool_calls": []map[string]any{{
		"index": 0, "id": "c1", "type": "function",
		"function": map[string]any{"name": name, "arguments": args},
	}}}, "")
}

// Every kind the gateway emits arrives as its own kind.
//
// The discriminator is the point: before ai-gateway#81 the shape was inferred
// from which fields happened to be non-nil, and a streamed tool call reached
// clients as an empty delta. A client that re-infers would reintroduce that.
func TestChatStream_DeliversEachKindFromTheRealFrameShapes(t *testing.T) {
	c := sseServer(t,
		frame(map[string]any{"reasoning_content": "thinking"}, ""),
		frame(map[string]any{"content": "hello "}, ""),
		toolFrame("get_weather", `{"city":"Paris"}`),
		frame(map[string]any{}, "tool_calls"),
		"[DONE]",
	)

	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)

	if len(got) != 4 {
		t.Fatalf("got %d chunks, want 4: %+v", len(got), got)
	}
	if got[0].Kind != StreamReasoning || got[0].Text != "thinking" {
		t.Errorf("chunk 0 = %+v, want reasoning", got[0])
	}
	if got[1].Kind != StreamText || got[1].Text != "hello " {
		t.Errorf("chunk 1 = %+v, want text", got[1])
	}
	if got[2].Kind != StreamToolCall || got[2].ToolCall == nil {
		t.Fatalf("chunk 2 = %+v, want a tool call", got[2])
	}
	if got[2].ToolCall.Name != "get_weather" {
		t.Errorf("tool name = %q", got[2].ToolCall.Name)
	}
	// The arguments stay raw. Decoding them here would make this client
	// responsible for a shape the model defines.
	var args map[string]string
	if err := json.Unmarshal(got[2].ToolCall.Arguments, &args); err != nil {
		t.Fatalf("arguments are not usable JSON: %v", err)
	}
	if args["city"] != "Paris" {
		t.Errorf("arguments = %v", args)
	}
	if got[3].Kind != StreamDone || got[3].Finish != "tool_calls" {
		t.Errorf("chunk 3 = %+v, want done/tool_calls", got[3])
	}
}

// Reasoning never merges into the answer.
//
// THE ONE IRREVERSIBLE CHOICE, per the gateway's own note. Once folded into
// content there is no way to pull them apart, so a client that concatenates
// destroys the distinction for everything downstream — including a shell that
// wants to render thinking dimmed, or not at all.
func TestChatStream_ReasoningIsNeverMergedIntoTheAnswer(t *testing.T) {
	c := sseServer(t,
		frame(map[string]any{"reasoning_content": "the user wants weather"}, ""),
		frame(map[string]any{"content": "It is sunny."}, ""),
		"[DONE]",
	)
	ch, _ := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})

	var answer, thinking strings.Builder
	for _, c := range drain(t, ch) {
		switch c.Kind {
		case StreamText:
			answer.WriteString(c.Text)
		case StreamReasoning:
			thinking.WriteString(c.Text)
		}
	}
	if strings.Contains(answer.String(), "the user wants weather") {
		t.Errorf("reasoning leaked into the answer: %q", answer.String())
	}
	if thinking.String() == "" {
		t.Error("reasoning was dropped entirely; a client that wants to show it cannot")
	}
}

// An error frame is delivered, not swallowed.
//
// The gateway ends the stream after one. A client that treated that as a
// clean end would show a truncated answer as a finished one.
func TestChatStream_AnErrorFrameIsDeliveredNotSwallowed(t *testing.T) {
	c := sseServer(t,
		frame(map[string]any{"content": "partial"}, ""),
		errFrame("upstream_error", "supplier said no", true),
	)
	ch, _ := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	got := drain(t, ch)

	last := got[len(got)-1]
	if last.Kind != StreamError || last.Err == nil {
		t.Fatalf("last chunk = %+v, want an error", last)
	}
	if last.Err.Code != "upstream_error" || !last.Err.Retryable {
		t.Errorf("err = %+v, want the code and retryable to survive", last.Err)
	}
}

// A stream that stops without [DONE] is a truncation, not a clean end.
//
// This is the failure a caller can least detect for itself: the bytes simply
// stop, and a half-answer looks exactly like a short one.
func TestChatStream_ATruncatedStreamIsAnErrorNotACleanEnd(t *testing.T) {
	c := sseServer(t, frame(map[string]any{"content": "half an ans"}, "")) // no [DONE]
	ch, _ := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	got := drain(t, ch)

	last := got[len(got)-1]
	if last.Kind != StreamError || last.Err == nil || last.Err.Code != "stream_truncated" {
		t.Fatalf("last chunk = %+v, want stream_truncated: a stream that stops "+
			"without [DONE] left the turn incomplete", last)
	}
	if !last.Err.Retryable {
		t.Error("a truncation should be retryable")
	}
}

// A tool call whose arguments are not complete JSON is refused.
//
// THE ONE WITH THE WORST BLAST RADIUS. The gateway reassembles and promises a
// whole call per frame; if that promise breaks, the thing downstream of this
// client RUNS the call. Executing a tool with half its arguments is worse
// than refusing, so a broken promise surfaces rather than being absorbed into
// a plausible-looking object.
func TestChatStream_AnIncompleteToolCallIsRefusedNotExecuted(t *testing.T) {
	c := sseServer(t,
		toolFrame("write_file", `{"path":"/etc/pas`), // truncated on purpose
		"[DONE]",
	)
	ch, _ := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	got := drain(t, ch)

	for _, g := range got {
		if g.Kind == StreamToolCall {
			t.Fatalf("a tool call with truncated arguments was passed on: %s",
				g.ToolCall.Arguments)
		}
	}
	if got[0].Kind != StreamError || got[0].Err.Code != "tool_call_incomplete" {
		t.Fatalf("got %+v, want tool_call_incomplete", got[0])
	}
}

// The channel always closes, so a caller ranging over it cannot hang.
func TestChatStream_TheChannelAlwaysCloses(t *testing.T) {
	for name, frames := range map[string][]string{
		"clean":     {frame(map[string]any{"content": "hi"}, ""), "[DONE]"},
		"error":     {errFrame("x", "y", false)},
		"truncated": {frame(map[string]any{"content": "hi"}, "")},
		"empty":     {},
		"garbage":   {"not json at all"},
	} {
		t.Run(name, func(t *testing.T) {
			c := sseServer(t, frames...)
			ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { drain(t, ch); close(done) }()
			select {
			case <-done:
			case <-context.Background().Done():
				t.Fatal("unreachable")
			}
		})
	}
}

// A 401 on the stream is ErrUnauthenticated, like every other call.
//
// LOAD-BEARING, not tidiness. chatRetryingOnceOnStaleKey re-mints a spent
// session key and retries exactly once, and it keys on this sentinel. A
// stream that returned a bare error would skip that recovery, so a shell
// whose 24h key expired mid-session would fail with "the credential was not
// accepted" and no way forward — the defect ba-service#29 fixed for chat.
func TestChatStream_A401IsErrUnauthenticatedSoTheReMintPathApplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "spent", nil).ChatStream(context.Background(),
		ChatRequest{Model: "claude"})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated: the stream path must reach the "+
			"same re-mint recovery as chat", err)
	}
}

// Any other non-200 names the status rather than returning a nil channel.
func TestChatStream_ANonOKStatusIsReportedNotSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ch, err := New(srv.URL, "t", nil).ChatStream(context.Background(),
		ChatRequest{Model: "claude"})
	if err == nil {
		t.Fatal("a 502 produced no error")
	}
	if ch != nil {
		t.Error("a channel was returned alongside an error; a caller ranging over it " +
			"would block forever")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want the status named", err)
	}
}

// Cancelling stops the reader rather than leaking it.
//
// A shell must be able to interrupt a long turn. If send blocked forever on
// an unread channel the goroutine would outlive the request, and a session
// of interrupted turns would accumulate one reader each.
func TestChatStream_CancellingStopsTheReader(t *testing.T) {
	// A server that keeps writing until the client goes away.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i := 0; i < 10000; i++ {
			if _, err := fmt.Fprintf(w, "data: %s\n\n",
				frame(map[string]any{"content": "x"}, "")); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := New(srv.URL, "t", nil).ChatStream(ctx, ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	<-ch // take one, then stop reading
	cancel()

	// The channel must close rather than the goroutine parking on a send.
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reader did not stop after cancellation; it is leaked")
	}
}

// The error text says which stream failed and why.
func TestStreamErr_ReadsAsACauseNotACode(t *testing.T) {
	e := &StreamErr{Code: "upstream_error", Message: "supplier said no"}
	got := e.Error()
	for _, want := range []string{"upstream_error", "supplier said no"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing %q", got, want)
		}
	}
}

// A 503 is an outage, not a bad credential.
//
// The gateway renders every virtual-key refusal — malformed, unknown, wrong
// secret, revoked, expired — as a byte-identical 401 so the door is not a
// key-enumeration oracle (ai-gateway#93). That is right, and it means a user
// seeing 401 reasonably reaches for their key.
//
// 503 is the one refusal that is NOT about the caller. Collapsing it into
// the same message sends someone to rotate a credential during a database
// incident, which is work that cannot touch the fault.
func TestClient_A503IsAnOutageNotABadCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"auth backend unavailable"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t", nil).Usage(context.Background())
	if errors.Is(err, ErrUnauthenticated) {
		t.Fatal("a 503 was reported as a credential problem; the reader is sent to " +
			"rotate a key during an outage")
	}
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("err = %v, want ErrBackendUnavailable", err)
	}
	if !strings.Contains(err.Error(), "outage") {
		t.Errorf("err = %v, want it to say plainly that this is not the credential", err)
	}
}

// A scope refusal reaches a streaming caller with the gateway's own reason.
//
// THIS IS THE FAILURE MODE FOR A SCOPE PROBLEM, and the message is all the
// user gets. A missing leartechapi:gateway:chat now returns 403
// insufficient_scope rather than 401, so it is deliberately NOT re-minted
// — a second key would fail identically. That makes the text the whole of
// the remedy, and this path used to render it as "returned 403".
func TestChatStream_A403CarriesTheGatewaysReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"insufficient_scope","detail":"requires leartechapi:gateway:chat"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "k", nil).ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden so callers can tell it from a bad key", err)
	}
	if !strings.Contains(err.Error(), "leartechapi:gateway:chat") {
		t.Errorf("err = %v, want the gateway's reason, which is the only remedy the user gets", err)
	}
}

// An outage is not a bad credential, on this path either.
//
// Without this, a 503 during a shell session reads as "returned 503" and
// the user reaches for their key — the exact confusion ErrBackendUnavailable
// exists to prevent on the non-streaming path.
func TestChatStream_A503IsAnOutageNotABadCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "k", nil).ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("err = %v, want ErrBackendUnavailable", err)
	}
	if errors.Is(err, ErrUnauthenticated) {
		t.Error("a 503 must not read as an authentication problem")
	}
}

// A 401 keeps its reason too, so the re-mint path does not swallow it.
//
// The key here is a realistic 12-hex-char virtual key rather than "k".
// A one-character token makes the scrubber redact every "k" in the
// gateway's prose — true of the real scrubber, harmless for a real key,
// and a trap for anyone writing the next test on this path.
func TestChatStream_A401CarriesTheGatewaysReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid virtual key"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "6b264491a156", nil).ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated so the shell re-mints", err)
	}
	if !strings.Contains(err.Error(), "invalid virtual key") {
		t.Errorf("err = %v, want the gateway's reason carried", err)
	}
}

// The key never appears in an error from this path.
//
// The non-streaming path scrubs; this one returned the transport error
// unchanged, and a transport error can quote the request.
func TestChatStream_AnErrorNeverQuotesTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"denied for key sk-live-SECRET123"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "sk-live-SECRET123", nil).ChatStream(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if strings.Contains(err.Error(), "sk-live-SECRET123") {
		t.Errorf("the key reached an error message: %v", err)
	}
}

// Two tool calls that both claim "index": 0 are both delivered.
//
// THE WIRE IS WRONG RIGHT NOW AND THIS CLIENT SURVIVES IT. The gateway
// hardcodes index 0 on every streamed tool call (ai-gateway, the
// ChunkToolCall branch), so a parallel pair arrives as two entries both
// claiming the same index. The OpenAI contract says a client accumulates
// tool-call deltas KEYED BY INDEX, and a conforming accumulator merges
// those two: either concatenating the arguments into invalid JSON, or
// overwriting the first call and silently losing it. The second is the
// dangerous one — valid JSON, one call, and the model asked for two.
//
// This client does not accumulate at all. The gateway reassembles
// fragments (#81/#83), so each entry is treated as a complete call and
// its arguments are validated as JSON. That makes index irrelevant here
// and the client immune to both failure modes.
//
// THE COST OF THAT IMMUNITY, stated so nobody mistakes it for conformance:
// a green parallel run against this client does NOT prove the wire carries
// correct indices. It proves only that we never read them.
func TestChatStream_TwoToolCallsSharingAnIndexAreBothDelivered(t *testing.T) {
	c := sseServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_1","index":0,"function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_2","index":0,"function":{"name":"list_dir","arguments":"{\"path\":\"/tmp\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	)
	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var calls []ToolCall
	for _, g := range drain(t, ch) {
		if g.Kind == StreamToolCall {
			calls = append(calls, *g.ToolCall)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("got %d tool calls, want 2 — a shared index must not merge them", len(calls))
	}
	if calls[0].ID == calls[1].ID {
		t.Errorf("both calls have id %q; they are distinct calls", calls[0].ID)
	}
	if calls[0].Name != "read_file" || calls[1].Name != "list_dir" {
		t.Errorf("names = %q, %q; want read_file then list_dir in arrival order",
			calls[0].Name, calls[1].Name)
	}
	// The failure that would be silent: arguments concatenated into one.
	for _, c := range calls {
		if !json.Valid(c.Arguments) {
			t.Errorf("%s arguments are not valid JSON: %s", c.Name, c.Arguments)
		}
	}
}

// capturingSSEServer records the request body so a test can assert what the
// client ASKED for, not only what it did with the reply.
func capturingSSEServer(t *testing.T, body *[]byte, frames ...string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*body = b
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "t", nil)
}

// usageFrame builds the trailing usage chunk the way the gateway does: an
// EMPTY choices slice plus a usage object.
//
// source: leartech-ai-gateway internal/api/handlers.go streamChat —
// `emitUsage := func(u Usage) { frame([]map[string]any{}, u) }`.
func usageFrame(prompt, completion, cachedRead, write5m int) string {
	b, err := json.Marshal(map[string]any{
		"id": "x", "object": "chat.completion.chunk", "created": 1,
		"model": "claude", "choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens": prompt, "completion_tokens": completion,
			"total_tokens":          prompt + completion,
			"prompt_tokens_details": map[string]any{"cached_tokens": cachedRead},
			"leartech_cache": map[string]any{
				"write_5m_tokens": write5m, "write_1h_tokens": 0,
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// A stream must ASK for usage or the gateway never sends it.
//
// This is the half that cannot be caught by parsing: a perfect parser sees
// nothing if the request omitted stream_options, which is how the shell ran
// blind to cost while the one-shot path reported it.
func TestChatStream_AsksForUsage(t *testing.T) {
	var body []byte
	c := capturingSSEServer(t, &body, frame(map[string]any{}, "stop"), "[DONE]")

	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)

	var sent struct {
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("request body did not parse: %v", err)
	}
	if sent.StreamOptions == nil {
		t.Fatal("the request carried no stream_options, so the gateway will send no usage frame")
	}
	if !sent.StreamOptions.IncludeUsage {
		t.Error("stream_options.include_usage = false, so usage is never emitted")
	}
}

// Usage rides an EMPTY frame that arrives AFTER the finish reason.
//
// Two ways this silently fails and both are real: the frame is skipped by an
// empty-choices guard, or the reader stops at the finish reason and never
// reads it.
func TestChatStream_UsageArrivesAfterTheFinishReason(t *testing.T) {
	c := sseServer(t,
		frame(map[string]any{"content": "hi"}, ""),
		frame(map[string]any{}, "stop"),
		usageFrame(4300, 120, 4096, 0),
		"[DONE]",
	)

	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)

	var usage *ChatUsage
	for _, c := range got {
		if c.Kind == StreamUsage {
			usage = c.Usage
		}
	}
	if usage == nil {
		t.Fatalf("no StreamUsage chunk; the usage frame was dropped: %+v", got)
	}
	if usage.PromptTokens != 4300 || usage.CompletionTokens != 120 {
		t.Errorf("tokens = %d/%d, want 4300/120", usage.PromptTokens, usage.CompletionTokens)
	}
	if !usage.CacheReported() {
		t.Error("CacheReported() = false on a frame that carried cache detail")
	}
	if usage.CachedTokens() != 4096 {
		t.Errorf("CachedTokens() = %d, want 4096", usage.CachedTokens())
	}
	// The usage frame must not be mistaken for the end of the turn.
	if last := got[len(got)-1]; last.Kind != StreamUsage {
		t.Errorf("last chunk = %v, want the usage chunk to arrive last", last.Kind)
	}
}

// A supplier that cannot cache reports NO cache detail, and absent must stay
// distinguishable from zero all the way through the stream.
func TestChatStream_AnUncachedSupplierReportsAbsentNotZero(t *testing.T) {
	noDetail, err := json.Marshal(map[string]any{
		"id": "x", "object": "chat.completion.chunk", "created": 1,
		"model": "qwen", "choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := sseServer(t, frame(map[string]any{}, "stop"), string(noDetail), "[DONE]")

	ch, cerr := c.ChatStream(context.Background(), ChatRequest{Model: "qwen"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	for _, chunk := range drain(t, ch) {
		if chunk.Kind != StreamUsage {
			continue
		}
		if chunk.Usage.CacheReported() {
			t.Error("CacheReported() = true for a supplier that sent no cache detail")
		}
		return
	}
	t.Fatal("no usage chunk arrived")
}

// The gateway HONOURS an inbound X-Request-ID, so a client can make its
// own logs joinable to the gateway's without the gateway changing.
//
// source: leartech-ai-gateway internal/server/middleware.go RequestID —
// `id := c.GetHeader("X-Request-ID"); if id == "" { id = randHex(8) }`.
func TestChatStream_CarriesTheCorrelationID(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(headerSessionID)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", frame(map[string]any{}, "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "t", nil).Correlate(
		func() (string, string) { return "sess-abc-3", CorrelateSession })
	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)

	if got != "sess-abc-3" {
		t.Errorf("%s = %q, want the correlation id; the gateway's usage rows "+
			"cannot be tied to a session without it", headerSessionID, got)
	}
}

// Without a correlator the header must be absent so the gateway generates
// its own, rather than receiving an empty string.
func TestCorrelate_AbsentLeavesTheHeadersUnset(t *testing.T) {
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["X-Leartech-Session-Id"]
		_, alsoRun := r.Header["X-Leartech-Run-Id"]
		present = present || alsoRun
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "t", nil)
	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if present {
		t.Error("a correlation header was sent with no correlator configured")
	}
}

// A session that failed to register has no id. Sending an empty header is
// worse than sending none: the gateway would record "" as the request id
// and every unregistered session would collide on it.
func TestCorrelate_AnEmptyIDIsNotSent(t *testing.T) {
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["X-Leartech-Session-Id"]
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "t", nil).Correlate(
		func() (string, string) { return "", CorrelateSession })
	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if present {
		t.Error("an empty correlation id was sent as a header")
	}
}

// AN UNKNOWN KIND SENDS NOTHING. Falling through to whichever header came
// last would file a session as a run, or the reverse.
func TestCorrelate_AnUnknownKindIsNotSent(t *testing.T) {
	var any bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, s := r.Header["X-Leartech-Session-Id"]
		_, ru := r.Header["X-Leartech-Run-Id"]
		any = s || ru
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "t", nil).Correlate(
		func() (string, string) { return "real-id", "invented-kind" })
	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if any {
		t.Error("a correlation header was sent for an unrecognised kind")
	}
}

// A scripted run's inherited id goes on the RUN header.
func TestCorrelate_StampsTheRunHeaderForAScriptedRun(t *testing.T) {
	var run, sess string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run = r.Header.Get(headerRunID)
		sess = r.Header.Get(headerSessionID)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "t", nil).Correlate(
		func() (string, string) { return "agentrun-7", CorrelateRun })
	ch, err := c.ChatStream(context.Background(), ChatRequest{Model: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if run != "agentrun-7" {
		t.Errorf("%s = %q, want agentrun-7", headerRunID, run)
	}
	if sess != "" {
		t.Errorf("%s = %q, want the session header left alone", headerSessionID, sess)
	}
}

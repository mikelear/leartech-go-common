package wshub_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mikelear/leartech-go-common/pkg/auth"
	"github.com/mikelear/leartech-go-common/pkg/wshub"
)

// stubVerifier accepts exactly one token string and binds it to a subject.
type stubVerifier struct {
	good string
	sub  string
}

func (s stubVerifier) DecodeToken(token string) (*auth.TokenClaims, error) {
	if token != s.good {
		return nil, errors.New("bad token")
	}
	return &auth.TokenClaims{UserID: s.sub}, nil
}

func serverFor(t *testing.T, v wshub.Verifier, h wshub.Handler) *httptest.Server {
	t.Helper()
	return httptest.NewServer(wshub.Serve(wshub.Options{
		Verifier:       v,
		OriginPatterns: []string{"*"}, // test client sends no Origin; allow all here
	}, h))
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func TestHandler_RejectsAMissingOrBadToken(t *testing.T) {
	v := stubVerifier{good: "tok", sub: "user-1"}
	called := false
	srv := serverFor(t, v, func(ctx context.Context, c *wshub.Conn) { called = true })
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// No token at all → handshake refused.
	_, _, err := websocket.Dial(ctx, wsURL(srv.URL), nil)
	if err == nil {
		t.Fatal("expected dial to fail without a bearer")
	}

	// Wrong token via subprotocol → refused.
	_, _, err = websocket.Dial(ctx, wsURL(srv.URL), &websocket.DialOptions{
		Subprotocols: []string{"bearer.nope"},
	})
	if err == nil {
		t.Fatal("expected dial to fail with a bad bearer")
	}
	if called {
		t.Fatal("handler must not run for an unauthenticated connection")
	}
}

func TestHandler_AcceptsAValidTokenAndBindsTheSubject(t *testing.T) {
	v := stubVerifier{good: "tok", sub: "user-42"}
	gotSub := make(chan string, 1)
	srv := serverFor(t, v, func(ctx context.Context, c *wshub.Conn) {
		gotSub <- c.Subject
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURL(srv.URL), &websocket.DialOptions{
		Subprotocols: []string{"bearer.tok"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	select {
	case sub := <-gotSub:
		if sub != "user-42" {
			t.Fatalf("subject = %q, want the token's sub user-42", sub)
		}
	case <-ctx.Done():
		t.Fatal("handler never ran for a valid connection")
	}
}

func TestHandler_ReadsAndWritesTextFrames(t *testing.T) {
	v := stubVerifier{good: "tok", sub: "user-1"}
	srv := serverFor(t, v, func(ctx context.Context, c *wshub.Conn) {
		// Echo one message back, upper-cased-ish (prove both directions).
		msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		_ = c.Write(ctx, append([]byte("echo:"), msg...))
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURL(srv.URL), &websocket.DialOptions{
		Subprotocols: []string{"bearer.tok"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if err := c.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText || string(data) != "echo:hello" {
		t.Fatalf("got %q (type %v), want text echo:hello", string(data), typ)
	}
}

func TestBearerFrom_PrefersTheAuthorizationHeader(t *testing.T) {
	// A non-browser caller using the header must be accepted too.
	v := stubVerifier{good: "tok", sub: "svc-1"}
	ran := make(chan struct{}, 1)
	srv := serverFor(t, v, func(ctx context.Context, c *wshub.Conn) { ran <- struct{}{} })
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURL(srv.URL), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer tok"}},
	})
	if err != nil {
		t.Fatalf("dial with header: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	select {
	case <-ran:
	case <-ctx.Done():
		t.Fatal("handler never ran for a header-bearer connection")
	}
}

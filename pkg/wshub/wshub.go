// Package wshub is a small, auth-checked WebSocket endpoint for streaming to a
// browser.
//
// WHY IT EXISTS. A browser SPA that wants a streamed, bidirectional channel to a
// Go service hits two walls that SSE does not clear — a browser EventSource
// carries no Authorization header, and it is one-way.
// Every leartech service that streams to a browser — the Ship Proven web
// console via leartech-console-api first — would otherwise re-solve the same
// three problems: authenticate the handshake with the estate's Verifier, pump
// frames both ways without leaking a goroutine, and close cleanly. This hub is
// that solution, once, so a future service mounts it rather than copying it.
//
// WHAT IT IS NOT. It is deliberately thin: accept, AUTHENTICATE, hand the caller
// a Conn bound to the verified subject, and pump. It knows nothing about agent
// loops, sessions or gateways — that is the service's job (leartech-console-api
// drives agent-core over a Conn this hub produced). Keeping the transport free
// of the application is what lets the application be tested without a socket and
// the socket be tested without the application.
package wshub

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/mikelear/leartech-go-common/pkg/auth"
)

// Verifier is the subset of *auth.Verifier the hub needs: decode-and-validate a
// bearer string into claims. An interface, not the concrete type, so a test can
// authenticate without a live JWKS.
//
// proven-by: TestHandler_RejectsAMissingOrBadToken
type Verifier interface {
	DecodeToken(token string) (*auth.TokenClaims, error)
}

// Conn is one authenticated client connection, bound to the subject the
// handshake proved. The service reads messages the browser sent and writes
// frames back; it never sees the raw socket.
type Conn struct {
	// Subject is the verified `sub` of the handshake token — who this
	// connection speaks for. Set from the verified token's sub, not from
	// anything the client sends.
	// proven-by: TestHandler_AcceptsAValidTokenAndBindsTheSubject
	Subject string
	// Claims is the full validated claim set, for a service that gates on scope
	// or reads tenant/role.
	Claims *auth.TokenClaims

	// Bearer is the validated handshake token, kept so a service that mints a
	// DOWNSTREAM credential on the user's behalf (e.g. console-api minting a
	// per-session gateway key) can present it. Not included in any ServerFrame.
	Bearer string

	ws *websocket.Conn
}

// Read blocks for the next text message from the client, returning its bytes.
// A closed connection returns an error the caller treats as "client gone",
// which is the normal end of every healthy session, not a fault.
func (c *Conn) Read(ctx context.Context) ([]byte, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, errors.New("wshub: expected a text frame")
	}
	return data, nil
}

// Write sends one text frame to the client.
func (c *Conn) Write(ctx context.Context, data []byte) error {
	return c.ws.Write(ctx, websocket.MessageText, data)
}

// Close closes the connection with a normal-closure status.
func (c *Conn) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// CloseError reports the connection closed with an error status and a reason,
// for the case where the service wants the client to see WHY (an auth expiry
// mid-stream, a budget exhaustion).
func (c *Conn) CloseError(reason string) error {
	return c.ws.Close(websocket.StatusInternalError, truncateReason(reason))
}

// Options configures a Handler.
type Options struct {
	// Verifier authenticates the handshake bearer. Required.
	Verifier Verifier
	// OriginPatterns is the CORS allow-list for the WebSocket upgrade — the
	// browser Origin values permitted to connect. REQUIRED and non-empty: the
	// default (empty) would accept any origin, which is cross-site hijacking of
	// an authenticated socket. The deploy passes the console's host.
	OriginPatterns []string
	// HandshakeTimeout bounds the upgrade. Zero uses a sane default.
	HandshakeTimeout time.Duration
}

// Handler is the connection callback: the hub calls it ONCE per authenticated
// connection, on the connection's own goroutine, and closes the socket when it
// returns. The service puts its per-connection logic (drive the loop, pump the
// session) here.
type Handler func(ctx context.Context, conn *Conn)

// Serve returns an http.HandlerFunc that upgrades to WebSocket, authenticates
// the handshake, and runs h with an authenticated Conn.
//
// AUTHENTICATION IS AT THE HANDSHAKE, BEFORE ANY APPLICATION FRAME. The bearer
// arrives either as the `Authorization: Bearer` header (non-browser clients) or
// as a `bearer.<token>` `Sec-WebSocket-Protocol` value (the subprotocol header,
// which a browser WebSocket can set). Either way it is decoded and validated by the
// Verifier before the upgrade completes; a missing or bad token is a 401 and no
// socket is opened.
//
// proven-by: TestHandler_RejectsAMissingOrBadToken
// proven-by: TestHandler_AcceptsAValidTokenAndBindsTheSubject
// proven-by: TestHandler_ReadsAndWritesTextFrames
func Serve(opts Options, h Handler) http.HandlerFunc {
	if opts.HandshakeTimeout == 0 {
		opts.HandshakeTimeout = 10 * time.Second
	}
	return func(w http.ResponseWriter, r *http.Request) {
		token, subproto := bearerFrom(r)
		if token == "" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		if opts.Verifier == nil {
			http.Error(w, "server misconfigured: no verifier", http.StatusInternalServerError)
			return
		}
		claims, err := opts.Verifier.DecodeToken(token)
		if err != nil || claims == nil || claims.UserID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		acceptOpts := &websocket.AcceptOptions{OriginPatterns: opts.OriginPatterns}
		// When the token came in via the subprotocol, echo that subprotocol back
		// so the browser's WebSocket negotiation succeeds.
		if subproto != "" {
			acceptOpts.Subprotocols = []string{subproto}
		}
		ws, err := websocket.Accept(w, r, acceptOpts)
		if err != nil {
			// Accept already wrote the HTTP error (e.g. origin not allowed).
			return
		}

		conn := &Conn{Subject: claims.UserID, Claims: claims, Bearer: token, ws: ws}
		// The handler owns the connection's lifetime; close when it returns.
		defer func() { _ = conn.Close() }()
		h(r.Context(), conn)
	}
}

// bearerFrom extracts the handshake bearer and, when it came via the
// subprotocol, the subprotocol string to echo back. Order: Authorization header
// first (non-browser callers), then a `bearer.<token>` subprotocol (browsers).
func bearerFrom(r *http.Request) (token, subproto string) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer "), ""
	}
	for _, p := range websocketSubprotocols(r) {
		if strings.HasPrefix(p, "bearer.") {
			return strings.TrimPrefix(p, "bearer."), p
		}
	}
	return "", ""
}

// websocketSubprotocols parses the comma-separated Sec-WebSocket-Protocol header.
func websocketSubprotocols(r *http.Request) []string {
	raw := r.Header.Get("Sec-WebSocket-Protocol")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func truncateReason(s string) string {
	// WebSocket close reasons are capped at 123 bytes by the protocol.
	if len(s) <= 123 {
		return s
	}
	return s[:120] + "..."
}

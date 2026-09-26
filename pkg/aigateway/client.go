package aigateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var (
	// ErrNoCredential reports that there is no token to present.
	ErrNoCredential = errors.New("gateway: no credential to present")
	// ErrUnauthenticated reports a 401: the credential was not accepted.
	ErrUnauthenticated = errors.New("gateway: the credential was not accepted")

	// ErrBackendUnavailable reports a 503: the gateway's auth backend is
	// down. Distinct from ErrUnauthenticated because the remedy is to wait,
	// not to rotate anything.
	ErrBackendUnavailable = errors.New("gateway: the auth backend is unavailable; " +
		"this is an outage, not a problem with your credential")
	// ErrForbidden reports a 403: the credential was accepted and the
	// request was refused.
	//
	// Deliberately says nothing about WHY. This read "the credential is
	// valid but lacks the scope", which named one of at least three causes
	// -- the gateway also 403s for a model outside the key's allowlist and
	// for an exhausted budget. A real model_not_allowed refusal was
	// therefore reported as a scope problem, and the scope was fine.
	//
	// The cause travels in the wrapped text, from the gateway. It is not
	// this sentinel's to guess.
	//
	// proven-by: TestChat_ModelNotAllowedSurfacesTheGatewaysOwnReason
	// proven-by: TestRefusals_403DoesNotAssertACause
	ErrForbidden = errors.New("gateway: request refused")
)

// Client talks to the gateway's admin API as one bearer credential.
type Client struct {
	base      string
	token     string
	http      *http.Client
	correlate func() (value, kind string)
}

// New returns a Client presenting token at base.
func New(base, token string, h *http.Client) *Client {
	if h == nil {
		h = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(base, "/"), token: token, http: h}
}

// Correlation headers the gateway records as its own join keys.
//
// NOT X-Request-ID, WHICH WAS THE FIRST ATTEMPT AND DOES NOT SURVIVE. The
// gateway honours an inbound X-Request-ID, so supplying one looked
// sufficient — but Envoy Gateway fronts the public hostname and replaces
// that header for external callers. Probed on 2026-09-26:
//
//	curl -D - -H 'X-Request-ID: probe-123' https://<gateway>/healthz
//	  -> x-request-id: 26c6d246-c5bd-421d-94ba-a159acd2cc9a
//
// The turns were metered correctly and were unattributable. These two
// names belong to the estate rather than to Envoy, so they arrive intact.
//
// source: leartech-ai-gateway internal/server/middleware.go Correlation,
// which reads both and logs them as run_id and session_id on the usage
// line as well as http_request.
const (
	headerRunID     = "X-Leartech-Run-Id"
	headerSessionID = "X-Leartech-Session-Id"
)

// Correlate stamps the caller's join keys on every request from id().
//
// THE JOIN KEY BETWEEN TWO SERVICES' LOGS. The shell talks to the gateway
// DIRECTLY for chat and to ba-service only for sessions, so a shell
// session's cost lives in one service's logs and its messaging in
// another's, with nothing in common.
//
// WHICH HEADER DEPENDS ON WHAT THE ID IS. A registered session id joins to
// ba-service's own session_id lines; an inherited LEARTECH_RUN_ID joins to
// an AgentRun. Sending a session id as run_id would make a shell session
// look like an agent run to the estate's forensic tooling, so the caller
// says which kind it has.
//
// Nil sends neither, which is the right default for one-shot commands with
// nothing to correlate to.
//
// proven-by: TestCorrelate_StampsTheSessionHeader
// proven-by: TestCorrelate_StampsTheRunHeaderForAScriptedRun
// proven-by: TestCorrelate_AbsentLeavesTheHeadersUnset
// proven-by: TestChatStream_CarriesTheCorrelationID
func (c *Client) Correlate(id func() (value, kind string)) *Client {
	c.correlate = id
	return c
}

// stamp sets the correlation header when one is configured and non-empty.
//
// AN EMPTY STRING IS NOT A CORRELATION ID. A session that failed to
// register has no id, and sending the header empty would have the gateway
// record "" as this caller's run — a value every uncorrelated caller in the
// estate would share. Sending nothing leaves it absent, which is what the
// gateway's allowlist does with it anyway.
//
// proven-by: TestCorrelate_AnEmptyIDIsNotSent
// proven-by: TestCorrelate_AnUnknownKindIsNotSent
func (c *Client) stamp(h http.Header) {
	if c.correlate == nil {
		return
	}
	value, kind := c.correlate()
	if value == "" {
		return
	}
	switch kind {
	case CorrelateSession:
		h.Set(headerSessionID, value)
	case CorrelateRun:
		h.Set(headerRunID, value)
	}
}

// The kinds of id a caller can correlate by.
//
// A STRING PAIR RATHER THAN TWO FUNCTIONS, because exactly one applies per
// run: a session id when one was registered, otherwise an inherited run
// id. Two setters would let a caller send both and leave the gateway to
// guess which is authoritative.
const (
	CorrelateSession = "session"
	CorrelateRun     = "run"
)

// apiError covers both error shapes the gateway uses.
//
// /v1/* returns the OpenAI shape, where `error` is an OBJECT:
//
//	{"error":{"message":"model \"qwen\" is not allowed","type":"model_not_allowed"}}
//
// /admin/v1/* returns a flat one. This was typed with `error` as a string,
// so the nested shape failed to unmarshal, reason() returned "", and the
// client printed its own guess instead -- "the credential lacks the scope"
// for a 403 that was actually about a model or a budget. The gateway said
// exactly what was wrong and the client threw it away.
//
// Same defect as reading scp as a string: a field typed for one shape
// yields nothing for the other, and nothing renders as an absent reason
// rather than a parse failure.
//
// proven-by: TestReason_ReadsBothErrorShapesTheGatewayUses
type apiError struct {
	Error   json.RawMessage `json:"error"`
	Message string          `json:"message"`
	Detail  string          `json:"detail"`
}

type nestedError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// errorText pulls the most specific message out of whichever shape arrived.
func (e apiError) errorText() string {
	if len(e.Error) > 0 {
		var n nestedError
		if err := json.Unmarshal(e.Error, &n); err == nil {
			switch {
			case n.Message != "" && n.Type != "":
				return n.Type + ": " + n.Message
			case n.Message != "":
				return n.Message
			case n.Type != "":
				return n.Type
			}
		}
		var flat string
		if err := json.Unmarshal(e.Error, &flat); err == nil && flat != "" {
			// Both halves. The flat shape puts a code in `error` and the
			// scope it wants in `detail`, so returning only the code
			// dropped "requires leartechapi:gateway:keys_write" -- the one
			// part a reader can act on.
			if e.Detail != "" {
				return flat + ": " + e.Detail
			}
			return flat
		}
	}
	if e.Detail != "" {
		return e.Detail
	}
	return e.Message
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {

	if c.token == "" {
		return ErrNoCredential
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gateway: encode the request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("gateway: build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.stamp(req.Header)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gateway: %s %s: %w", method, path, c.scrubErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("gateway: read %s %s: %w", method, path, err)
	}

	if err := c.statusError(method, path, resp.StatusCode, raw); err != nil {
		return err
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gateway: %s %s returned %d with a body that is not the expected JSON: %w",
			method, path, resp.StatusCode, err)
	}
	return nil
}

// statusError renders a refusal, for EVERY path that can receive one.
//
// SHARED BECAUSE IT DIVERGED ONCE. The streaming path grew its own
// handling, which returned a bare ErrUnauthenticated with no reason, and
// mapped 403 and 503 to a generic "returned %d" — so a scope refusal ended
// a shell session with a status code and nothing to act on, while the same
// refusal through Chat named the scope. One renderer, or it happens again.
//
// proven-by: TestChatStream_A403CarriesTheGatewaysReason
// proven-by: TestChatStream_A503IsAnOutageNotABadCredential
// proven-by: TestChatStream_A401CarriesTheGatewaysReason
func (c *Client) statusError(method, path string, status int, raw []byte) error {
	switch {
	case status == http.StatusUnauthorized:
		return fmt.Errorf("%w: %s %s said 401%s", ErrUnauthenticated, method, path, c.reason(raw))
	case status == http.StatusForbidden:
		// The gateway 403s for a missing scope AND for model_not_allowed,
		// no_limit_policy and no_spend_cap. Naming scope would send the
		// reader to the wrong place three times out of four, so the
		// gateway's own reason is the message.
		return fmt.Errorf("%w: %s %s said 403%s", ErrForbidden, method, path, c.reason(raw))
	case status == http.StatusServiceUnavailable:
		// proven-by: TestClient_A503IsAnOutageNotABadCredential
		//
		// The ONE refusal that is not about the caller: every virtual-key
		// refusal is a byte-identical 401 by design, so a user seeing one
		// reaches for their key. A 503 is the backend being down.
		return fmt.Errorf("%w: %s %s returned 503%s", ErrBackendUnavailable,
			method, path, c.reason(raw))
	case status >= 400:
		return fmt.Errorf("gateway: %s %s returned %d%s", method, path, status, c.reason(raw))
	}
	return nil
}

func (c *Client) reason(raw []byte) string {
	var e apiError
	if err := json.Unmarshal(raw, &e); err != nil {
		return ""
	}
	if t := e.errorText(); t != "" {
		return ": " + c.scrub(t)
	}
	return ""
}

func (c *Client) scrub(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

func (c *Client) scrubErr(e error) error { return errors.New(c.scrub(e.Error())) }

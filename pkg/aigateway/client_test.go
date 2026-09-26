package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "eyJhbGciOiJSUzI1NiJ9.THIS-IS-A-LIVE-BEARER-TOKEN.sig"

type captured struct {
	authorization string
	method        string
	path          string
	calls         int
}

func serve(t *testing.T, status int, body string) (*httptest.Server, *captured) {
	t.Helper()
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.calls++
		got.authorization = r.Header.Get("Authorization")
		got.method = r.Method
		got.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestListKeys_PresentsTheTokenAsABearerCredential(t *testing.T) {
	srv, got := serve(t, 200, `{"keys":[{"keyid":"k-1","name":"ba"}]}`)

	keys, err := New(srv.URL, testToken, srv.Client()).ListKeys(context.Background())
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if got.authorization != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want the bearer token", got.authorization)
	}
	if got.path != "/admin/v1/keys" {
		t.Errorf("path = %q", got.path)
	}
	if len(keys) != 1 || keys[0].KeyID != "k-1" {
		t.Errorf("keys = %+v", keys)
	}
}

// 401 and 403 are different problems with different fixes: log in again
// versus ask for a scope. Collapsing them sends the reader to the wrong
// place, and the estate's posture is that each refusal code stays
// distinguishable.
func TestRefusals_401And403AreDistinguishable(t *testing.T) {
	un, _ := serve(t, 401, `{"error":"invalid_token"}`)
	err := New(un.URL, testToken, un.Client()).RevokeKey(context.Background(), "k-1")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("401 gave %v, want ErrUnauthenticated", err)
	}
	if errors.Is(err, ErrForbidden) {
		t.Error("401 also matches ErrForbidden — the two are not distinguishable")
	}

	fb, _ := serve(t, 403, `{"error":"insufficient_scope","detail":"requires leartechapi:gateway:keys_write"}`)
	err = New(fb.URL, testToken, fb.Client()).RevokeKey(context.Background(), "k-1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("403 gave %v, want ErrForbidden", err)
	}
	if errors.Is(err, ErrUnauthenticated) {
		t.Error("403 also matches ErrUnauthenticated")
	}
	if !strings.Contains(err.Error(), "keys_write") {
		t.Errorf("403 error %q does not name the scope the gateway said was missing", err)
	}
}

// An absent credential is refused HERE, without a request.
//
// The gateway answers 401 to an empty bearer, and that 401 is
// indistinguishable from an expired login — so the reader goes to the issuer
// when the actual cause is an empty session file. Proven by the call count:
// asserting only the error would pass for a client that sent the request and
// relayed the 401.
func TestNoCredential_IsRefusedWithoutSendingARequest(t *testing.T) {
	srv, got := serve(t, 200, `{"keys":[]}`)

	_, err := New(srv.URL, "", srv.Client()).ListKeys(context.Background())
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	if got.calls != 0 {
		t.Errorf("%d request(s) were sent with no credential", got.calls)
	}

	// The matched pair: the same call WITH a token does reach the server.
	if _, err := New(srv.URL, testToken, srv.Client()).ListKeys(context.Background()); err != nil {
		t.Fatalf("the same call with a credential failed: %v", err)
	}
	if got.calls != 1 {
		t.Errorf("calls = %d after a credentialled request, want 1", got.calls)
	}
}

// Nothing the client prints may contain the bearer token it is holding —
// including when the gateway echoes it back.
func TestErrors_NeverContainTheBearerToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"401 echoing the token", 401, `{"error":"invalid_token","detail":"rejected ` + testToken + `"}`},
		{"403 echoing the token", 403, `{"error":"forbidden","detail":"` + testToken + ` lacks the scope"}`},
		{"500 echoing the token", 500, `{"message":"upstream saw ` + testToken + `"}`},
		{"a 200 that is not JSON", 200, `<html>` + testToken + `</html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := serve(t, tc.status, tc.body)
			_, err := New(srv.URL, testToken, srv.Client()).ListKeys(context.Background())
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Fatalf("the error carries the bearer token: %q", err)
			}
		})
	}
}

// The minted secret is the one value that must reach the caller intact — it
// is shown once and never again. Pinned so a future blanket redaction does
// not swallow it: the rule is "never PRINT a credential", and returning it
// to the caller that asked to mint it is not printing.
func TestCreateKey_ReturnsTheMintedSecretIntact(t *testing.T) {
	const secret = "sk-lt-MINTED-ONCE"
	srv, _ := serve(t, 200, `{"keyid":"k-9","secret":"`+secret+`","key":{"keyid":"k-9","name":"new"}}`)

	got, err := New(srv.URL, testToken, srv.Client()).CreateKey(context.Background(), CreateKeyRequest{Name: "new"})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if got.Secret != secret {
		t.Errorf("Secret = %q, want the minted value — it is shown once and cannot be recovered", got.Secret)
	}
}

// A 200 whose body is not the expected JSON is an error, not an empty
// result. An empty key list is a real and reassuring answer; "the response
// did not parse" is not, and the two must not look the same.
func TestListKeys_A200ThatDoesNotParseIsAnErrorNotAnEmptyList(t *testing.T) {
	srv, _ := serve(t, 200, `<html>gateway timeout page</html>`)

	keys, err := New(srv.URL, testToken, srv.Client()).ListKeys(context.Background())
	if err == nil {
		t.Fatalf("an unparseable 200 was reported as %d keys", len(keys))
	}

	// The matched pair: a genuine empty list IS an empty list, with no error.
	ok, _ := serve(t, 200, `{"keys":[]}`)
	keys, err = New(ok.URL, testToken, ok.Client()).ListKeys(context.Background())
	if err != nil {
		t.Fatalf("a genuinely empty list was reported as an error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("keys = %+v, want none", keys)
	}
}

func TestRoutes_MatchTheGatewaysOwnWiring(t *testing.T) {
	for _, tc := range []struct {
		name         string
		call         func(*Client) error
		method, path string
		// Each endpoint's own envelope. `{}` for all of them let a client
		// that decoded the WRONG envelope pass this test, because it only
		// looked at method and path. amend is the one that shipped wrong.
		body string
	}{
		{"list", func(c *Client) error { _, e := c.ListKeys(context.Background()); return e },
			http.MethodGet, "/admin/v1/keys", `{}`},
		{"create", func(c *Client) error {
			_, e := c.CreateKey(context.Background(), CreateKeyRequest{Name: "n"})
			return e
		},
			http.MethodPost, "/admin/v1/keys", `{}`},
		{"rotate", func(c *Client) error { _, e := c.RotateKey(context.Background(), "k-9"); return e },
			http.MethodPost, "/admin/v1/keys/k-9/rotate", `{}`},
		{"amend", func(c *Client) error { _, e := c.AmendKey(context.Background(), "k-9", AmendKeyRequest{}); return e },
			http.MethodPatch, "/admin/v1/keys/k-9", `{"keys":[{"keyid":"k-9"}]}`},
		{"revoke", func(c *Client) error { return c.RevokeKey(context.Background(), "k-9") },
			http.MethodDelete, "/admin/v1/keys/k-9", `{}`},
		{"usage", func(c *Client) error { _, e := c.Usage(context.Background()); return e },
			http.MethodGet, "/admin/v1/usage", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := serve(t, 200, tc.body)
			if err := tc.call(New(srv.URL, testToken, srv.Client())); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			if got.method != tc.method || got.path != tc.path {
				t.Errorf("sent %s %s, want %s %s", got.method, got.path, tc.method, tc.path)
			}
		})
	}
}

// A 403 must not name a cause the gateway did not give.
func TestRefusals_403DoesNotAssertACause(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"model", `{"error":{"message":"model \"echo\" is not allowed","type":"model_not_allowed"}}`, "model_not_allowed"},
		{"budget", `{"error":{"message":"budget exhausted","type":"budget_exceeded"}}`, "budget_exceeded"},
		{"scope", `{"error":"insufficient_scope","detail":"requires leartechapi:gateway:keys_write"}`, "keys_write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := serve(t, 403, tc.body)
			err := New(srv.URL, testToken, srv.Client()).RevokeKey(context.Background(), "k-1")
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("want ErrForbidden, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q drops the gateway's reason %q", err, tc.want)
			}
			if tc.name != "scope" && strings.Contains(err.Error(), "scope") {
				t.Errorf("a %s refusal reported as a scope problem: %v", tc.name, err)
			}
		})
	}
}

// Both error shapes the gateway uses decode to a usable reason.
//
// `error` was typed as a string. /v1/* sends it as an OBJECT, so the
// unmarshal failed, the reason came back empty, and the client printed its
// own guess over the top of it.
func TestReason_ReadsBothErrorShapesTheGatewayUses(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"nested, /v1/*", `{"error":{"message":"model \"echo\" is not allowed","type":"model_not_allowed"}}`,
			`model_not_allowed: model "echo" is not allowed`},
		{"nested, message only", `{"error":{"message":"budget exhausted"}}`, "budget exhausted"},
		{"nested, type only", `{"error":{"type":"model_not_allowed"}}`, "model_not_allowed"},
		{"flat, /admin/v1/*", `{"error":"insufficient_scope"}`, "insufficient_scope"},
		{"flat with detail", `{"error":"insufficient_scope","detail":"requires leartechapi:gateway:keys_write"}`,
			"insufficient_scope: requires leartechapi:gateway:keys_write"},
		{"detail only", `{"detail":"nope"}`, "nope"},
		{"message only", `{"message":"nope"}`, "nope"},
		{"nothing usable", `{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e apiError
			if err := json.Unmarshal([]byte(tc.body), &e); err != nil {
				t.Fatalf("the shape itself does not decode: %v", err)
			}
			if got := e.errorText(); got != tc.want {
				t.Errorf("errorText() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The correlation header rides the admin path too, so a `usage` or `keys`
// call made during a session is attributable to it.
func TestCorrelate_StampsTheSessionHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(headerSessionID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, testToken, srv.Client()).Correlate(
		func() (string, string) { return "sess-xyz-1", CorrelateSession })
	if _, err := c.ListKeys(context.Background()); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if got != "sess-xyz-1" {
		t.Errorf("%s = %q, want the correlation id", headerSessionID, got)
	}
}

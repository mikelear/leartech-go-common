package aigateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The version question carries no credential.
//
// It has to be answerable before login, because that is the only way to tell
// "this gateway is too old" apart from "this credential is wrong".
func TestVersion_IsAskedWithoutSendingACredential(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"version":"v0.0.60","api_level":2}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "a-real-token", srv.Client()).Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sawAuth != "" {
		t.Errorf("sent Authorization %q; /version must work without one", sawAuth)
	}
	if got.APILevel != 2 || got.Version != "v0.0.60" {
		t.Fatalf("decoded %+v", got)
	}
}

// A gateway without the endpoint reports level 0, meaning "did not say".
//
// Not level 1: a caller has to be able to tell a silent gateway from one that
// answered, or it cannot decide whether to stay quiet.
func TestVersion_AGatewayWithoutTheEndpointReportsLevelZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := New(srv.URL, "t", srv.Client()).Version(context.Background())
	if err != nil {
		t.Fatalf("a 404 is an answer, not an error: %v", err)
	}
	if got.APILevel != 0 {
		t.Fatalf("api_level = %d, want 0 for a gateway that does not report", got.APILevel)
	}
}

func TestVersion_ABrokenBodyIsAnErrorNotLevelZero(t *testing.T) {
	// Level 0 means "did not say". HTML from a proxy is a different
	// problem and must not be silently flattened into it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>gateway timeout</html>"))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "t", srv.Client()).Version(context.Background()); err == nil {
		t.Fatal("an unparseable body decoded as a version")
	}
}

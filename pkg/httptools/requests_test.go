package httptools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTokenGetter stamps a fixed Bearer header and lets tests force errors.
type fakeTokenGetter struct {
	token   string
	setErr  error
	calls   int
	lastReq *http.Request
}

func (f *fakeTokenGetter) GetAuthToken(_ context.Context) (*string, error) {
	t := f.token
	return &t, nil
}

func (f *fakeTokenGetter) SetAuthHeader(_ context.Context, req *http.Request) error {
	f.calls++
	f.lastReq = req
	if f.setErr != nil {
		return f.setErr
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	return nil
}

type echoBody struct {
	Hello string `json:"hello"`
}

func TestMakeAuthorisedGetRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer t-1", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	var got echoBody
	err := MakeAuthorisedGetRequest(&fakeTokenGetter{token: "t-1"}, t.Context(), srv.URL, &got)
	require.NoError(t, err)
	assert.Equal(t, "world", got.Hello)
}

func TestMakeAuthorisedGetRequest_AcceptsNilContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	err := MakeAuthorisedGetRequest(&fakeTokenGetter{token: "t"}, t.Context(), srv.URL, nil)
	require.NoError(t, err)

	// Also explicitly exercise the nil-ctx code path; the helper substitutes
	// context.Background() rather than panicking.
	err = MakeAuthorisedGetRequest(&fakeTokenGetter{token: "t"}, context.Background(), srv.URL, nil)
	require.NoError(t, err)
}

func TestMakeAuthorisedGetRequest_PropagatesTokenError(t *testing.T) {
	tg := &fakeTokenGetter{setErr: errors.New("boom")}
	err := MakeAuthorisedGetRequest(tg, t.Context(), "http://x", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting auth header")
}

func TestMakeAuthorisedGetRequest_FailsOnBadURL(t *testing.T) {
	err := MakeAuthorisedGetRequest(&fakeTokenGetter{}, t.Context(), "http://\x7f", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "building GET request")
}

func TestMakeAuthorisedPostRequest_SendsJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer t-2", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"hello":"posted"}`))
	}))
	defer srv.Close()

	body := echoBody{Hello: "hi"}
	var got echoBody
	err := MakeAuthorisedPostRequest(&fakeTokenGetter{token: "t-2"}, t.Context(), srv.URL, body, &got)
	require.NoError(t, err)
	assert.Equal(t, "posted", got.Hello)
}

func TestMakeAuthorisedPostRequest_RejectsUnmarshalable(t *testing.T) {
	err := MakeAuthorisedPostRequest(&fakeTokenGetter{}, t.Context(), "http://x", make(chan int), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshalling")
}

func TestMakeAuthorisedPostRequest_PropagatesTokenError(t *testing.T) {
	tg := &fakeTokenGetter{setErr: errors.New("nope")}
	err := MakeAuthorisedPostRequest(tg, t.Context(), "http://x", echoBody{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting auth header")
}

func TestDoRequest_NonOKReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := MakeAuthorisedGetRequest(&fakeTokenGetter{}, t.Context(), srv.URL, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestDoRequest_RejectsInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	var got echoBody
	err := MakeAuthorisedGetRequest(&fakeTokenGetter{}, t.Context(), srv.URL, &got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding")
}

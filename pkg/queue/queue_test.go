package queue

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newTestQueue[T any](t *testing.T) Queue[T] {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisQueue[T](client)
}

func TestQueue_PushPopRoundTrip(t *testing.T) {
	q := newTestQueue[item](t)

	in := item{ID: "1", Name: "alpha"}
	require.NoError(t, q.Push(t.Context(), "k", in))

	out, err := q.Pop(t.Context(), "k")
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestQueue_FIFO(t *testing.T) {
	q := newTestQueue[item](t)

	require.NoError(t, q.Push(t.Context(), "k", item{ID: "1"}))
	require.NoError(t, q.Push(t.Context(), "k", item{ID: "2"}))
	require.NoError(t, q.Push(t.Context(), "k", item{ID: "3"}))

	for _, want := range []string{"1", "2", "3"} {
		got, err := q.Pop(t.Context(), "k")
		require.NoError(t, err)
		assert.Equal(t, want, got.ID)
	}
}

func TestQueue_Len(t *testing.T) {
	q := newTestQueue[item](t)

	n, err := q.Len(t.Context(), "k")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	require.NoError(t, q.Push(t.Context(), "k", item{ID: "a"}))
	require.NoError(t, q.Push(t.Context(), "k", item{ID: "b"}))

	n, err = q.Len(t.Context(), "k")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestQueue_PopEmptyReturnsError(t *testing.T) {
	q := newTestQueue[item](t)

	_, err := q.Pop(t.Context(), "nothing")
	require.Error(t, err)
}

func TestQueue_GenericWithPrimitive(t *testing.T) {
	q := newTestQueue[string](t)

	require.NoError(t, q.Push(t.Context(), "k", "hello"))
	got, err := q.Pop(t.Context(), "k")
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

package redis

import (
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	idx := strings.LastIndex(addr, ":")
	require.NotEqual(t, -1, idx)
	port, err := strconv.Atoi(addr[idx+1:])
	require.NoError(t, err)
	return addr[:idx], port
}

func TestNewRedisClient_PingsOnConnect(t *testing.T) {
	mr := miniredis.RunT(t)
	host, port := splitHostPort(t, mr.Addr())

	client, err := NewRedisClient(Config{Host: host, Port: port})
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Drive a real op to confirm the client is functional, not just ping-passed.
	require.NoError(t, client.Set(t.Context(), "k", "v", 0).Err())
	got, err := client.Get(t.Context(), "k").Result()
	require.NoError(t, err)
	assert.Equal(t, "v", got)
}

func TestNewRedisClient_FailsWhenUnreachable(t *testing.T) {
	// Port 1 is reserved; nothing listens there.
	client, err := NewRedisClient(Config{Host: "127.0.0.1", Port: 1})
	assert.Nil(t, client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis ping failed")
}

func TestNewRedisClient_HonoursDBSelection(t *testing.T) {
	mr := miniredis.RunT(t)
	host, port := splitHostPort(t, mr.Addr())

	client, err := NewRedisClient(Config{Host: host, Port: port, DB: 3})
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	require.NoError(t, client.Set(t.Context(), "x", "in-db-3", 0).Err())

	// Key landed in DB 3, not the default DB 0.
	got, err := mr.DB(3).Get("x")
	require.NoError(t, err)
	assert.Equal(t, "in-db-3", got)
	assert.False(t, mr.DB(0).Exists("x"), "key must not exist in DB 0")
}

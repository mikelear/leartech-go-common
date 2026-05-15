package lock

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLocker(t *testing.T, ttl time.Duration, prefix string) (Locker, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisLock(client, ttl, prefix), mr
}

func TestAcquireLock_FirstCallerWins(t *testing.T) {
	locker, _ := newTestLocker(t, time.Minute, "test")

	assert.True(t, locker.AcquireLock(t.Context(), "thing"))
	assert.False(t, locker.AcquireLock(t.Context(), "thing"), "second acquire of same key must fail")
}

func TestAcquireLock_DifferentKeysIndependent(t *testing.T) {
	locker, _ := newTestLocker(t, time.Minute, "test")

	assert.True(t, locker.AcquireLock(t.Context(), "a"))
	assert.True(t, locker.AcquireLock(t.Context(), "b"))
}

func TestReleaseLock_AllowsReacquire(t *testing.T) {
	locker, _ := newTestLocker(t, time.Minute, "test")

	require.True(t, locker.AcquireLock(t.Context(), "k"))
	require.NoError(t, locker.ReleaseLock(t.Context(), "k"))
	assert.True(t, locker.AcquireLock(t.Context(), "k"), "must reacquire after release")
}

func TestAcquireLock_ExpiresAfterTTL(t *testing.T) {
	locker, mr := newTestLocker(t, time.Minute, "test")

	require.True(t, locker.AcquireLock(t.Context(), "k"))

	mr.FastForward(2 * time.Minute) // miniredis time travel

	assert.True(t, locker.AcquireLock(t.Context(), "k"), "TTL expiry must permit reacquire")
}

func TestLockKey_PrefixApplied(t *testing.T) {
	locker, mr := newTestLocker(t, time.Minute, "myprefix")
	require.True(t, locker.AcquireLock(t.Context(), "thing"))

	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "myprefix:thing", keys[0])
}

func TestLockKey_NoPrefix(t *testing.T) {
	locker, mr := newTestLocker(t, time.Minute, "")
	require.True(t, locker.AcquireLock(t.Context(), "raw"))

	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "raw", keys[0])
}

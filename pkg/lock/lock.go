// Package lock provides distributed locking via Redis.
// Replaces spring-financial-group/mqube-go-common/pkg/lock.
package lock

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

// Locker provides distributed lock acquisition and release.
type Locker interface {
	AcquireLock(ctx context.Context, key string) bool
	ReleaseLock(ctx context.Context, key string) error
}

type redisLock struct {
	client *goredis.Client
	ttl    time.Duration
	prefix string
}

// NewRedisLock creates a Redis-backed distributed lock.
func NewRedisLock(client *goredis.Client, ttl time.Duration, prefix string) Locker {
	return &redisLock{
		client: client,
		ttl:    ttl,
		prefix: prefix,
	}
}

func (l *redisLock) lockKey(key string) string {
	if l.prefix != "" {
		return l.prefix + ":" + key
	}
	return key
}

func (l *redisLock) AcquireLock(ctx context.Context, key string) bool {
	ok, err := l.client.SetNX(ctx, l.lockKey(key), "locked", l.ttl).Result()
	if err != nil {
		log.Error().Err(err).Str("key", key).Msg("failed to acquire lock")
		return false
	}
	return ok
}

func (l *redisLock) ReleaseLock(ctx context.Context, key string) error {
	return l.client.Del(ctx, l.lockKey(key)).Err()
}

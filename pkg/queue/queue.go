// Package queue provides a generic Redis-backed queue.
// Replaces spring-financial-group/mqube-go-common/pkg/queue.
package queue

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// Queue is a generic FIFO queue backed by Redis lists.
type Queue[T any] interface {
	Push(ctx context.Context, key string, value T) error
	Pop(ctx context.Context, key string) (T, error)
	Len(ctx context.Context, key string) (int64, error)
}

type redisQueue[T any] struct {
	client *goredis.Client
}

// NewRedisQueue creates a new Redis-backed generic queue.
func NewRedisQueue[T any](client *goredis.Client) Queue[T] {
	return &redisQueue[T]{client: client}
}

func (q *redisQueue[T]) Push(ctx context.Context, key string, value T) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshalling queue item: %w", err)
	}
	return q.client.RPush(ctx, key, data).Err()
}

func (q *redisQueue[T]) Pop(ctx context.Context, key string) (T, error) {
	var zero T
	result, err := q.client.LPop(ctx, key).Result()
	if err != nil {
		return zero, err
	}

	var value T
	if err := json.Unmarshal([]byte(result), &value); err != nil {
		return zero, fmt.Errorf("unmarshalling queue item: %w", err)
	}
	return value, nil
}

func (q *redisQueue[T]) Len(ctx context.Context, key string) (int64, error) {
	return q.client.LLen(ctx, key).Result()
}

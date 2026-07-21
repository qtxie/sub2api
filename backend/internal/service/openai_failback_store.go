package service

import (
	"context"
	"time"
)

// OpenAIFailbackStore is an optional distributed store implemented by the
// gateway Redis cache. Keeping it separate avoids expanding GatewayCache and
// every cache test double.
type OpenAIFailbackStore interface {
	GetOpenAIFailbackState(ctx context.Context, key string) (value string, found bool, err error)
	CompareAndSwapOpenAIFailbackState(ctx context.Context, key, expected, next string, ttl time.Duration) (bool, error)
	AcquireOpenAIFailbackProbe(ctx context.Context, key, owner string, ttl time.Duration) (bool, error)
	ReleaseOpenAIFailbackProbe(ctx context.Context, key, owner string) error
}

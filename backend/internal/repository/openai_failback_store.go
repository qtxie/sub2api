package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openAIFailbackKeyPrefix = "openai_failback:"

var compareAndSwapOpenAIFailbackStateScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if ARGV[1] == '' then
  if current then return 0 end
elseif current ~= ARGV[1] then
  return 0
end
if ARGV[2] == '' then
  redis.call('DEL', KEYS[1])
else
  redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
end
return 1
`)

var releaseOpenAIFailbackProbeScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

var _ service.OpenAIFailbackStore = (*gatewayCache)(nil)

func openAIFailbackStateKey(key string) string {
	return openAIFailbackKeyPrefix + "state:" + key
}

func openAIFailbackProbeKey(key string) string {
	return openAIFailbackKeyPrefix + "probe:" + key
}

func (c *gatewayCache) GetOpenAIFailbackState(ctx context.Context, key string) (string, bool, error) {
	value, err := c.rdb.Get(ctx, openAIFailbackStateKey(key)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (c *gatewayCache) CompareAndSwapOpenAIFailbackState(
	ctx context.Context,
	key, expected, next string,
	ttl time.Duration,
) (bool, error) {
	ttlMS := ttl.Milliseconds()
	if ttlMS <= 0 {
		return false, fmt.Errorf("OpenAI failback state TTL must be positive")
	}
	result, err := compareAndSwapOpenAIFailbackStateScript.Run(
		ctx,
		c.rdb,
		[]string{openAIFailbackStateKey(key)},
		expected,
		next,
		ttlMS,
	).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *gatewayCache) AcquireOpenAIFailbackProbe(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, openAIFailbackProbeKey(key), owner, ttl).Result()
}

func (c *gatewayCache) ReleaseOpenAIFailbackProbe(ctx context.Context, key, owner string) error {
	return releaseOpenAIFailbackProbeScript.Run(
		ctx,
		c.rdb,
		[]string{openAIFailbackProbeKey(key)},
		owner,
	).Err()
}

package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const stickySessionPrefix = "sticky_session:"
const openAIFailbackStatePrefix = "openai:failback_state:"

var recordOpenAIFailbackScript = redis.NewScript(`
local current = tonumber(redis.call('HGET', KEYS[1], 'cooldown_seconds') or '0')
if current <= 0 then current = tonumber(ARGV[2]) end
redis.call('HSET', KEYS[1],
  'cooldown_seconds', current,
  'cooldown_until_ms', 0,
  'last_failback_ms', ARGV[1],
  'fast_count', 0)
redis.call('EXPIRE', KEYS[1], ARGV[3])
return {current, 0, tonumber(ARGV[1]), 0}
`)

var recordOpenAIFailbackFailureScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local base = tonumber(ARGV[2])
local increment = tonumber(ARGV[3])
local maximum = tonumber(ARGV[4])
local relapse = tonumber(ARGV[5])
local current = tonumber(redis.call('HGET', KEYS[1], 'cooldown_seconds') or '0')
local last = tonumber(redis.call('HGET', KEYS[1], 'last_failback_ms') or '0')
local until_ms = tonumber(redis.call('HGET', KEYS[1], 'cooldown_until_ms') or '0')
local fast_count = tonumber(redis.call('HGET', KEYS[1], 'fast_count') or '0')
if last <= 0 then
  return {current, until_ms, last, fast_count}
end
if current <= 0 then current = base end
if last > 0 and now - last <= relapse then
  current = current + increment
  if maximum > 0 and current > maximum then current = maximum end
else
  current = base
end
until_ms = now + current * 1000
redis.call('HSET', KEYS[1],
  'cooldown_seconds', current,
  'cooldown_until_ms', until_ms,
  'last_failback_ms', 0,
  'fast_count', 0)
redis.call('EXPIRE', KEYS[1], ARGV[6])
return {current, until_ms, 0, 0}
`)

var recordOpenAIFailbackProductionScript = redis.NewScript(`
local last = tonumber(redis.call('HGET', KEYS[1], 'last_failback_ms') or '0')
local current = tonumber(redis.call('HGET', KEYS[1], 'cooldown_seconds') or '0')
local until_ms = tonumber(redis.call('HGET', KEYS[1], 'cooldown_until_ms') or '0')
local fast_count = tonumber(redis.call('HGET', KEYS[1], 'fast_count') or '0')
local reset = 0
if last > 0 then
  if tonumber(ARGV[1]) == 1 then fast_count = fast_count + 1 else fast_count = 0 end
  if fast_count >= tonumber(ARGV[2]) then
    current = tonumber(ARGV[3])
    last = 0
    fast_count = 0
    reset = 1
  end
  redis.call('HSET', KEYS[1],
    'cooldown_seconds', current,
    'cooldown_until_ms', until_ms,
    'last_failback_ms', last,
    'fast_count', fast_count)
  redis.call('EXPIRE', KEYS[1], ARGV[4])
end
return {current, until_ms, last, fast_count, reset}
`)

var reconcileOpenAIFailbackStateScript = redis.NewScript(`
local local_current = tonumber(ARGV[1])
local local_until = tonumber(ARGV[2])
local local_last = tonumber(ARGV[3])
local local_fast = tonumber(ARGV[4])
local current = local_current
local until_ms = local_until
local last = local_last
local fast_count = local_fast
if redis.call('EXISTS', KEYS[1]) == 1 then
  local remote_current = tonumber(redis.call('HGET', KEYS[1], 'cooldown_seconds') or '0')
  local remote_until = tonumber(redis.call('HGET', KEYS[1], 'cooldown_until_ms') or '0')
  local remote_last = tonumber(redis.call('HGET', KEYS[1], 'last_failback_ms') or '0')
  local remote_fast = tonumber(redis.call('HGET', KEYS[1], 'fast_count') or '0')
  if remote_current > current then current = remote_current end
  if remote_until > until_ms then until_ms = remote_until end
  if remote_last > local_last then
    last = remote_last
    fast_count = remote_fast
  elseif remote_last == local_last and remote_last > 0 and remote_fast < fast_count then
    -- Be conservative after a partition: never manufacture fast successes.
    fast_count = remote_fast
  end
end
redis.call('HSET', KEYS[1],
  'cooldown_seconds', current,
  'cooldown_until_ms', until_ms,
  'last_failback_ms', last,
  'fast_count', fast_count)
redis.call('EXPIRE', KEYS[1], ARGV[5])
return {current, until_ms, last, fast_count}
`)

type gatewayCache struct {
	rdb *redis.Client
}

func NewGatewayCache(rdb *redis.Client) service.GatewayCache {
	return &gatewayCache{rdb: rdb}
}

// buildSessionKey 构建 session key，包含 groupID 实现分组隔离
// 格式: sticky_session:{groupID}:{sessionHash}
func buildSessionKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", stickySessionPrefix, groupID, sessionHash)
}

func (c *gatewayCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Get(ctx, key).Int64()
}

func (c *gatewayCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Set(ctx, key, accountID, ttl).Err()
}

func (c *gatewayCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// DeleteSessionAccountID 删除粘性会话与账号的绑定关系。
// 当检测到绑定的账号不可用（如状态错误、禁用、不可调度等）时调用，
// 以便下次请求能够重新选择可用账号。
//
// DeleteSessionAccountID removes the sticky session binding for the given session.
// Called when the bound account becomes unavailable (e.g., error status, disabled,
// or unschedulable), allowing subsequent requests to select a new available account.
func (c *gatewayCache) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Del(ctx, key).Err()
}

func openAIFailbackStateKey(accountID int64) string {
	return fmt.Sprintf("%s%d", openAIFailbackStatePrefix, accountID)
}

func openAIFailbackRedisInt(value any) int64 {
	switch value := value.(type) {
	case int64:
		return value
	case string:
		parsed, _ := strconv.ParseInt(value, 10, 64)
		return parsed
	case []byte:
		parsed, _ := strconv.ParseInt(string(value), 10, 64)
		return parsed
	default:
		return 0
	}

}

func openAIFailbackStateFromValues(values []any) service.OpenAIFailbackState {
	readInt := func(index int) int64 {
		if index >= len(values) {
			return 0
		}
		return openAIFailbackRedisInt(values[index])
	}
	state := service.OpenAIFailbackState{
		CooldownSeconds: int(readInt(0)),
		FastCount:       int(readInt(3)),
	}
	if millis := readInt(1); millis > 0 {
		state.CooldownUntil = time.UnixMilli(millis)
	}
	if millis := readInt(2); millis > 0 {
		state.LastFailbackAt = time.UnixMilli(millis)
	}
	return state
}

func failbackTTLSeconds(ttl time.Duration) int64 {
	seconds := int64(ttl / time.Second)
	if seconds <= 0 {
		return 1
	}
	return seconds
}

func (c *gatewayCache) GetOpenAIFailbackState(ctx context.Context, accountID int64) (service.OpenAIFailbackState, error) {
	values, err := c.rdb.HMGet(ctx, openAIFailbackStateKey(accountID), "cooldown_seconds", "cooldown_until_ms", "last_failback_ms", "fast_count").Result()
	if err != nil {
		return service.OpenAIFailbackState{}, err
	}
	return openAIFailbackStateFromValues(values), nil
}

func (c *gatewayCache) RecordOpenAIFailback(ctx context.Context, accountID int64, at time.Time, baseCooldown, ttl time.Duration) (service.OpenAIFailbackState, error) {
	values, err := recordOpenAIFailbackScript.Run(ctx, c.rdb, []string{openAIFailbackStateKey(accountID)}, at.UnixMilli(), int64(baseCooldown/time.Second), failbackTTLSeconds(ttl)).Slice()
	if err != nil {
		return service.OpenAIFailbackState{}, err
	}
	return openAIFailbackStateFromValues(values), nil
}

func (c *gatewayCache) RecordOpenAIFailbackFailure(ctx context.Context, accountID int64, at time.Time, baseCooldown, increment, maxCooldown, relapseWindow, ttl time.Duration) (service.OpenAIFailbackState, error) {
	values, err := recordOpenAIFailbackFailureScript.Run(ctx, c.rdb, []string{openAIFailbackStateKey(accountID)},
		at.UnixMilli(), int64(baseCooldown/time.Second), int64(increment/time.Second), int64(maxCooldown/time.Second), relapseWindow.Milliseconds(), failbackTTLSeconds(ttl)).Slice()
	if err != nil {
		return service.OpenAIFailbackState{}, err
	}
	return openAIFailbackStateFromValues(values), nil
}

func (c *gatewayCache) RecordOpenAIFailbackProductionResult(ctx context.Context, accountID int64, _ time.Time, fast bool, recoveryFastCount int, baseCooldown, ttl time.Duration) (service.OpenAIFailbackState, bool, error) {
	fastValue := 0
	if fast {
		fastValue = 1
	}
	values, err := recordOpenAIFailbackProductionScript.Run(ctx, c.rdb, []string{openAIFailbackStateKey(accountID)},
		fastValue, recoveryFastCount, int64(baseCooldown/time.Second), failbackTTLSeconds(ttl)).Slice()
	if err != nil {
		return service.OpenAIFailbackState{}, false, err
	}
	state := openAIFailbackStateFromValues(values)
	reset := len(values) > 4 && openAIFailbackRedisInt(values[4]) == 1
	return state, reset, nil
}

func (c *gatewayCache) ReconcileOpenAIFailbackState(ctx context.Context, accountID int64, local service.OpenAIFailbackState, ttl time.Duration) (service.OpenAIFailbackState, error) {
	cooldownUntilMillis := int64(0)
	if !local.CooldownUntil.IsZero() {
		cooldownUntilMillis = local.CooldownUntil.UnixMilli()
	}
	lastFailbackMillis := int64(0)
	if !local.LastFailbackAt.IsZero() {
		lastFailbackMillis = local.LastFailbackAt.UnixMilli()
	}
	values, err := reconcileOpenAIFailbackStateScript.Run(ctx, c.rdb, []string{openAIFailbackStateKey(accountID)},
		local.CooldownSeconds, cooldownUntilMillis, lastFailbackMillis, local.FastCount, failbackTTLSeconds(ttl)).Slice()
	if err != nil {
		return service.OpenAIFailbackState{}, err
	}
	return openAIFailbackStateFromValues(values), nil
}

// Compile-time assertion: gatewayCache must implement CyberSessionBlockStore.
var _ service.CyberSessionBlockStore = (*gatewayCache)(nil)
var _ service.OpenAIFailbackStateCache = (*gatewayCache)(nil)
var _ service.OpenAIFailbackStateReconciler = (*gatewayCache)(nil)

const cyberSessionBlockPrefix = "cyber_session_block:"

// SetCyberSessionBlocked 把被 cyber_policy 命中的会话写入屏蔽表（TTL 自动过期）。
// 存储值 "1" 作为存在标记（IsCyberSessionBlocked 只检查 key 是否存在，不读值）。
func (c *gatewayCache) SetCyberSessionBlocked(ctx context.Context, key string, ttl time.Duration) error {
	return c.rdb.Set(ctx, cyberSessionBlockPrefix+key, "1", ttl).Err()
}

// IsCyberSessionBlocked 查询会话是否在屏蔽表中。
func (c *gatewayCache) IsCyberSessionBlocked(ctx context.Context, key string) (bool, error) {
	n, err := c.rdb.Exists(ctx, cyberSessionBlockPrefix+key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

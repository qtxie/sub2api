package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const stickySessionPrefix = "sticky_session:"
const liveCallPrefix = "live:call:"
const openAIAPIKeyRotationPrefix = "openai_api_key_rotation:"

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
	accountID, err := c.rdb.Get(ctx, key).Int64()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, service.ErrStickySessionNotFound
		}
		return 0, err
	}
	return accountID, nil
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

var resolveOpenAIAPIKeyIndexScript = redis.NewScript(`
	local fingerprint = redis.call('HGET', KEYS[1], 'fingerprint')
	local pool_size = tonumber(ARGV[2])
	if fingerprint ~= ARGV[1] then
		redis.call('HSET', KEYS[1], 'fingerprint', ARGV[1], 'active_index', 0, 'failures', 0, 'generation', 0)
		return {0, 0}
	end
	local active = tonumber(redis.call('HGET', KEYS[1], 'active_index')) or 0
	local generation = tonumber(redis.call('HGET', KEYS[1], 'generation')) or 0
	if active < 0 or active >= pool_size then
		redis.call('HSET', KEYS[1], 'active_index', 0, 'failures', 0, 'generation', generation + 1)
		return {0, generation + 1}
	end
	return {active, generation}
`)

var recordOpenAIAPIKeyStreamFailureScript = redis.NewScript(`
	local fingerprint = redis.call('HGET', KEYS[1], 'fingerprint')
	local attempted = tonumber(ARGV[2])
	local attempted_generation = tonumber(ARGV[3])
	local pool_size = tonumber(ARGV[4])
	local threshold = tonumber(ARGV[5])
	if fingerprint ~= ARGV[1] then
		return {-1, 0, -1, 0, 0}
	end
	local active = tonumber(redis.call('HGET', KEYS[1], 'active_index')) or 0
	local generation = tonumber(redis.call('HGET', KEYS[1], 'generation')) or 0
	if active < 0 or active >= pool_size then
		return {active, 0, generation, 0, 0}
	end
	local failures = tonumber(redis.call('HGET', KEYS[1], 'failures')) or 0
	if active ~= attempted or generation ~= attempted_generation then
		return {active, failures, generation, 0, 0}
	end
	failures = failures + 1
	if pool_size > 1 and failures >= threshold then
		active = (active + 1) % pool_size
		generation = generation + 1
		redis.call('HSET', KEYS[1], 'active_index', active, 'failures', 0, 'generation', generation)
		return {active, 0, generation, 1, 1}
	end
	redis.call('HSET', KEYS[1], 'failures', failures)
	return {active, failures, generation, 0, 1}
`)

var resetOpenAIAPIKeyStreamFailuresScript = redis.NewScript(`
	local fingerprint = redis.call('HGET', KEYS[1], 'fingerprint')
	if fingerprint ~= ARGV[1] then
		return {-1, 0}
	end
	local active = tonumber(redis.call('HGET', KEYS[1], 'active_index')) or 0
	local generation = tonumber(redis.call('HGET', KEYS[1], 'generation')) or 0
	if active ~= tonumber(ARGV[2]) or generation ~= tonumber(ARGV[3]) then
		return {-1, 0}
	end
	local failures = tonumber(redis.call('HGET', KEYS[1], 'failures')) or 0
	if failures > 0 then
		redis.call('HSET', KEYS[1], 'failures', 0)
	end
	return {failures, 1}
`)

func openAIAPIKeyRotationKey(accountID int64) string {
	return fmt.Sprintf("%s{%d}", openAIAPIKeyRotationPrefix, accountID)
}

func (c *gatewayCache) ResolveOpenAIAPIKeyIndex(ctx context.Context, accountID int64, poolFingerprint string, poolSize int) (service.OpenAIAPIKeyRotationSelection, error) {
	if poolSize <= 1 {
		return service.OpenAIAPIKeyRotationSelection{}, nil
	}
	values, err := resolveOpenAIAPIKeyIndexScript.Run(
		ctx,
		c.rdb,
		[]string{openAIAPIKeyRotationKey(accountID)},
		poolFingerprint,
		poolSize,
	).Int64Slice()
	if err != nil {
		return service.OpenAIAPIKeyRotationSelection{}, err
	}
	if len(values) != 2 {
		return service.OpenAIAPIKeyRotationSelection{}, fmt.Errorf("unexpected OpenAI API key selection result length: %d", len(values))
	}
	return service.OpenAIAPIKeyRotationSelection{ActiveIndex: int(values[0]), Generation: values[1]}, nil
}

func (c *gatewayCache) RecordOpenAIAPIKeyStreamFailure(
	ctx context.Context,
	accountID int64,
	poolFingerprint string,
	attemptedIndex int,
	attemptedGeneration int64,
	poolSize, threshold int,
) (service.OpenAIAPIKeyRotationResult, error) {
	values, err := recordOpenAIAPIKeyStreamFailureScript.Run(
		ctx,
		c.rdb,
		[]string{openAIAPIKeyRotationKey(accountID)},
		poolFingerprint,
		attemptedIndex,
		attemptedGeneration,
		poolSize,
		threshold,
	).Int64Slice()
	if err != nil {
		return service.OpenAIAPIKeyRotationResult{}, err
	}
	if len(values) != 5 {
		return service.OpenAIAPIKeyRotationResult{}, fmt.Errorf("unexpected OpenAI API key rotation result length: %d", len(values))
	}
	return service.OpenAIAPIKeyRotationResult{
		ActiveIndex:  int(values[0]),
		FailureCount: int(values[1]),
		Generation:   values[2],
		Rotated:      values[3] == 1,
		Recorded:     values[4] == 1,
	}, nil
}

func (c *gatewayCache) ResetOpenAIAPIKeyStreamFailures(
	ctx context.Context,
	accountID int64,
	poolFingerprint string,
	attemptedIndex int,
	attemptedGeneration int64,
) (previousFailures int, reset bool, err error) {
	values, err := resetOpenAIAPIKeyStreamFailuresScript.Run(
		ctx,
		c.rdb,
		[]string{openAIAPIKeyRotationKey(accountID)},
		poolFingerprint,
		attemptedIndex,
		attemptedGeneration,
	).Int64Slice()
	if err != nil {
		return 0, false, err
	}
	if len(values) != 2 {
		return 0, false, fmt.Errorf("unexpected OpenAI API key reset result length: %d", len(values))
	}
	return int(values[0]), values[1] == 1, nil
}

const (
	grokVideoPendingBillingPrefix = "grok_video_pending:"
	grokVideoBilledPrefix         = "grok_video_billed:"
)

func (c *gatewayCache) SetGrokVideoPendingBilling(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" || len(payload) == 0 {
		return errors.New("invalid grok video pending billing payload")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return c.rdb.Set(ctx, grokVideoPendingBillingPrefix+key, payload, ttl).Err()
}

func (c *gatewayCache) GetGrokVideoPendingBilling(ctx context.Context, key string) ([]byte, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("invalid grok video pending billing key")
	}
	val, err := c.rdb.Get(ctx, grokVideoPendingBillingPrefix+key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}

func (c *gatewayCache) ClaimGrokVideoBilled(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, errors.New("invalid grok video billed key")
	}
	if ttl <= 0 {
		ttl = 48 * time.Hour
	}
	return c.rdb.SetNX(ctx, grokVideoBilledPrefix+key, "1", ttl).Result()
}

func (c *gatewayCache) ReleaseGrokVideoBilled(ctx context.Context, key string) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("invalid grok video billed key")
	}
	return c.rdb.Del(ctx, grokVideoBilledPrefix+key).Err()
}

// Compile-time assertion: gatewayCache must implement CyberSessionBlockStore.
var _ service.CyberSessionBlockStore = (*gatewayCache)(nil)
var _ service.LiveCallStore = (*gatewayCache)(nil)
var _ service.OpenAIAPIKeyRotationStore = (*gatewayCache)(nil)

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

var claimLiveControllerScript = redis.NewScript(`
	local key = KEYS[1]
	local target = ARGV[1]
	local owner = ARGV[2]
	local current = redis.call('HGET', key, 'controller')
	if current == false or current == 'closed' then
		return 0
	end
	if target == 'observer' and current ~= 'pending' then
		return 0
	end
	if target == 'proxy' and current ~= 'pending' and current ~= 'observer' and
		(current ~= 'proxy' or redis.call('HGET', key, 'controller_owner') ~= owner) then
		return 0
	end
	redis.call('HSET', key, 'controller', target, 'controller_owner', owner)
	return 1
`)

var markLiveCallClosedScript = redis.NewScript(`
	local key = KEYS[1]
	if redis.call('EXISTS', key) == 0 then
		return 0
	end
	if redis.call('HGET', key, 'controller') == 'closed' then
		return 0
	end
	redis.call('HSET', key, 'controller', 'closed', 'controller_owner', '')
	redis.call('EXPIRE', key, ARGV[1])
	return 1
`)

var releaseLiveControllerScript = redis.NewScript(`
	local key = KEYS[1]
	if redis.call('HGET', key, 'controller') ~= 'proxy' or
		redis.call('HGET', key, 'controller_owner') ~= ARGV[1] then
		return 0
	end
	redis.call('HSET', key, 'controller', 'pending', 'controller_owner', '')
	return 1
`)

func liveCallKey(callHash string) string {
	return liveCallPrefix + callHash
}

func HashLiveCallID(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(sum[:])
}

func (c *gatewayCache) SaveLiveCall(ctx context.Context, record *service.LiveCallRecord, ttl time.Duration) error {
	if record == nil || record.CallHash == "" || record.CallID == "" {
		return fmt.Errorf("invalid live call record")
	}
	values := map[string]any{
		"call_id":          record.CallID,
		"account_id":       record.AccountID,
		"api_key_id":       record.APIKeyID,
		"user_id":          record.UserID,
		"group_id":         record.GroupID,
		"subscription_id":  record.SubscriptionID,
		"lease_id":         record.LeaseID,
		"model":            record.Model,
		"created_at":       record.CreatedAt.UnixMilli(),
		"expires_at":       record.ExpiresAt.UnixMilli(),
		"controller":       record.Controller,
		"controller_owner": record.ControllerOwner,
		"user_agent":       record.UserAgent,
		"ip_address":       record.IPAddress,
		"inbound_endpoint": record.InboundEndpoint,
		"attestation":      record.AttestationCiphertext,
	}
	key := liveCallKey(record.CallHash)
	pipe := c.rdb.TxPipeline()
	pipe.HSet(ctx, key, values)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *gatewayCache) GetLiveCall(ctx context.Context, callHash string) (*service.LiveCallRecord, error) {
	values, err := c.rdb.HGetAll(ctx, liveCallKey(callHash)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, service.ErrLiveCallNotFound
	}
	parseInt := func(field string) int64 {
		value, _ := strconv.ParseInt(values[field], 10, 64)
		return value
	}
	createdAt := time.UnixMilli(parseInt("created_at"))
	expiresAt := time.UnixMilli(parseInt("expires_at"))
	return &service.LiveCallRecord{
		CallID:                values["call_id"],
		CallHash:              callHash,
		AccountID:             parseInt("account_id"),
		APIKeyID:              parseInt("api_key_id"),
		UserID:                parseInt("user_id"),
		GroupID:               parseInt("group_id"),
		SubscriptionID:        parseInt("subscription_id"),
		LeaseID:               values["lease_id"],
		Model:                 values["model"],
		CreatedAt:             createdAt,
		ExpiresAt:             expiresAt,
		Controller:            values["controller"],
		ControllerOwner:       values["controller_owner"],
		UserAgent:             values["user_agent"],
		IPAddress:             values["ip_address"],
		InboundEndpoint:       values["inbound_endpoint"],
		AttestationCiphertext: values["attestation"],
	}, nil
}

func (c *gatewayCache) ClaimLiveController(ctx context.Context, callHash, controller, owner string) (bool, error) {
	result, err := claimLiveControllerScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, controller, owner).Int()
	return result == 1, err
}

func (c *gatewayCache) GetLiveController(ctx context.Context, callHash string) (string, error) {
	value, err := c.rdb.HGet(ctx, liveCallKey(callHash), "controller").Result()
	if err == redis.Nil {
		return "", service.ErrLiveCallNotFound
	}
	return value, err
}

func (c *gatewayCache) ReleaseLiveController(ctx context.Context, callHash, owner string) (bool, error) {
	result, err := releaseLiveControllerScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, owner).Int()
	return result == 1, err
}

func (c *gatewayCache) MarkLiveCallClosed(ctx context.Context, callHash string, ttl time.Duration) (bool, error) {
	result, err := markLiveCallClosedScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, int64(ttl.Seconds())).Int()
	return result == 1, err
}

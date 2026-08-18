package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	videoStudioTaskKeyPrefix  = "video_studio:task:"
	videoStudioClaimKeyPrefix = "video_studio:claim:"
	videoStudioPendingPrefix  = "video_studio:pending:"
	videoStudioDueKey         = "video_studio:tasks:due"
	videoStudioClaimScanLimit = 32
)

var videoStudioEnqueueScript = redis.NewScript(`
redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2])
redis.call("ZADD", KEYS[2], ARGV[3], ARGV[4])
redis.call("ZADD", KEYS[3], ARGV[5], ARGV[4])
local pending_ttl = redis.call("PTTL", KEYS[3])
if pending_ttl < tonumber(ARGV[2]) then
  redis.call("PEXPIRE", KEYS[3], ARGV[2])
end
return 1
`)

var videoStudioClaimDueScript = redis.NewScript(`
local ids = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", ARGV[1], "LIMIT", 0, ARGV[2])
for _, id in ipairs(ids) do
  local task_key = ARGV[3] .. id
  if redis.call("EXISTS", task_key) == 0 then
    redis.call("ZREM", KEYS[1], id)
  else
    local claim_key = ARGV[4] .. id
    if redis.call("SET", claim_key, ARGV[5], "NX", "PX", ARGV[6]) then
      redis.call("ZADD", KEYS[1], tonumber(ARGV[1]) + tonumber(ARGV[6]), id)
      return id
    end
  end
end
return nil
`)

var videoStudioCommitClaimScript = redis.NewScript(`
if redis.call("GET", KEYS[3]) ~= ARGV[1] then
  return 0
end
redis.call("SET", KEYS[1], ARGV[2], "PX", ARGV[3])
if ARGV[4] == "" then
  redis.call("ZREM", KEYS[2], ARGV[5])
  redis.call("ZREM", KEYS[4], ARGV[5])
else
  redis.call("ZADD", KEYS[2], ARGV[4], ARGV[5])
end
redis.call("DEL", KEYS[3])
return 1
`)

var videoStudioHasPendingScript = redis.NewScript(`
redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", ARGV[1])
return redis.call("ZCARD", KEYS[1])
`)

type videoStudioTaskStore struct {
	rdb *redis.Client
}

func NewVideoStudioTaskStore(rdb *redis.Client) service.VideoStudioTaskStore {
	return &videoStudioTaskStore{rdb: rdb}
}

func (s *videoStudioTaskStore) Enqueue(ctx context.Context, task *service.VideoStudioTask, ttl time.Duration, dueAt time.Time) error {
	if s == nil || s.rdb == nil || task == nil || !service.IsValidVideoStudioRequestID(task.RequestID) || task.UserID <= 0 || task.APIKeyID <= 0 {
		return errors.New("invalid video studio task")
	}
	if ttl <= 0 {
		ttl = time.Second
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	expiresAt := task.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(ttl)
	}
	_, err = videoStudioEnqueueScript.Run(ctx, s.rdb,
		[]string{videoStudioTaskKey(task.RequestID), videoStudioDueKey, videoStudioPendingKey(task.UserID, task.APIKeyID)},
		payload, ttl.Milliseconds(), dueAt.UnixMilli(), task.RequestID, expiresAt.UnixMilli(),
	).Result()
	return err
}

func (s *videoStudioTaskStore) HasPendingForAPIKey(ctx context.Context, userID, apiKeyID int64, now time.Time) (bool, error) {
	if s == nil || s.rdb == nil || userID <= 0 || apiKeyID <= 0 {
		return false, nil
	}
	count, err := videoStudioHasPendingScript.Run(
		ctx,
		s.rdb,
		[]string{videoStudioPendingKey(userID, apiKeyID)},
		now.UnixMilli(),
	).Int64()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *videoStudioTaskStore) Get(ctx context.Context, requestID string) (*service.VideoStudioTask, error) {
	if s == nil || s.rdb == nil || !service.IsValidVideoStudioRequestID(requestID) {
		return nil, service.ErrVideoStudioTaskNotFound
	}
	payload, err := s.rdb.Get(ctx, videoStudioTaskKey(requestID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, service.ErrVideoStudioTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	var task service.VideoStudioTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *videoStudioTaskStore) ClaimDue(ctx context.Context, now time.Time, lease time.Duration) (*service.VideoStudioTaskClaim, error) {
	if s == nil || s.rdb == nil {
		return nil, service.ErrVideoStudioQueueEmpty
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	token := uuid.NewString()
	raw, err := videoStudioClaimDueScript.Run(ctx, s.rdb, []string{videoStudioDueKey},
		now.UnixMilli(), videoStudioClaimScanLimit, videoStudioTaskKeyPrefix,
		videoStudioClaimKeyPrefix, token, lease.Milliseconds(),
	).Result()
	if errors.Is(err, redis.Nil) || raw == nil {
		return nil, service.ErrVideoStudioQueueEmpty
	}
	if err != nil {
		return nil, err
	}
	requestID, ok := raw.(string)
	if !ok || !service.IsValidVideoStudioRequestID(requestID) {
		return nil, service.ErrVideoStudioQueueEmpty
	}
	task, err := s.Get(ctx, requestID)
	if err != nil {
		_ = s.rdb.Del(ctx, videoStudioClaimKey(requestID)).Err()
		if errors.Is(err, service.ErrVideoStudioTaskNotFound) {
			_ = s.rdb.ZRem(ctx, videoStudioDueKey, requestID).Err()
			return nil, service.ErrVideoStudioQueueEmpty
		}
		return nil, err
	}
	return &service.VideoStudioTaskClaim{Task: task, Token: token}, nil
}

func (s *videoStudioTaskStore) CommitClaim(
	ctx context.Context,
	claim *service.VideoStudioTaskClaim,
	ttl time.Duration,
	nextPollAt *time.Time,
) error {
	if s == nil || s.rdb == nil || claim == nil || claim.Task == nil ||
		!service.IsValidVideoStudioRequestID(claim.Task.RequestID) || strings.TrimSpace(claim.Token) == "" {
		return service.ErrVideoStudioTaskClaimLost
	}
	if ttl <= 0 {
		ttl = time.Second
	}
	payload, err := json.Marshal(claim.Task)
	if err != nil {
		return err
	}
	nextScore := ""
	if nextPollAt != nil {
		nextScore = strconvFormatInt(nextPollAt.UnixMilli())
	}
	result, err := videoStudioCommitClaimScript.Run(ctx, s.rdb,
		[]string{
			videoStudioTaskKey(claim.Task.RequestID),
			videoStudioDueKey,
			videoStudioClaimKey(claim.Task.RequestID),
			videoStudioPendingKey(claim.Task.UserID, claim.Task.APIKeyID),
		},
		claim.Token, payload, ttl.Milliseconds(), nextScore, claim.Task.RequestID,
	).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return service.ErrVideoStudioTaskClaimLost
	}
	return nil
}

func videoStudioTaskKey(requestID string) string {
	return videoStudioTaskKeyPrefix + strings.TrimSpace(requestID)
}

func videoStudioClaimKey(requestID string) string {
	return videoStudioClaimKeyPrefix + strings.TrimSpace(requestID)
}

func videoStudioPendingKey(userID, apiKeyID int64) string {
	return videoStudioPendingPrefix + strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(apiKeyID, 10)
}

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

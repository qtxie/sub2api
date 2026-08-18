package repository

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type videoStudioFailNextGetHook struct {
	armed atomic.Bool
}

func newVideoStudioFailNextGetHook() *videoStudioFailNextGetHook {
	hook := &videoStudioFailNextGetHook{}
	hook.armed.Store(true)
	return hook
}

func (h *videoStudioFailNextGetHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *videoStudioFailNextGetHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "get" && h.armed.CompareAndSwap(true, false) {
			return errors.New("temporary redis read failure")
		}
		return next(ctx, cmd)
	}
}

func (h *videoStudioFailNextGetHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestVideoStudioTaskStoreLeaseRecoveryAndTerminalCommit(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := &videoStudioTaskStore{rdb: client}
	ctx := context.Background()
	now := time.Now().UTC()
	task := &service.VideoStudioTask{
		RequestID: "request-1", UserID: 1, APIKeyID: 2,
		Status: service.VideoStudioTaskStatusPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}

	require.NoError(t, store.Enqueue(ctx, task, time.Hour, now.Add(-time.Second)))
	pending, err := store.HasPendingForAPIKey(ctx, task.UserID, task.APIKeyID, now)
	require.NoError(t, err)
	require.True(t, pending)
	first, err := store.ClaimDue(ctx, now, 10*time.Second)
	require.NoError(t, err)
	require.Equal(t, task.RequestID, first.Task.RequestID)

	_, err = store.ClaimDue(ctx, now.Add(time.Second), 10*time.Second)
	require.ErrorIs(t, err, service.ErrVideoStudioQueueEmpty)

	mr.FastForward(11 * time.Second)
	second, err := store.ClaimDue(ctx, now.Add(11*time.Second), 10*time.Second)
	require.NoError(t, err)
	require.NotEqual(t, first.Token, second.Token)

	completedAt := now.Add(12 * time.Second)
	second.Task.Status = service.VideoStudioTaskStatusDone
	second.Task.CompletedAt = &completedAt
	require.NoError(t, store.CommitClaim(ctx, second, time.Hour, nil))
	pending, err = store.HasPendingForAPIKey(ctx, task.UserID, task.APIKeyID, now.Add(12*time.Second))
	require.NoError(t, err)
	require.False(t, pending)

	stored, err := store.Get(ctx, task.RequestID)
	require.NoError(t, err)
	require.Equal(t, service.VideoStudioTaskStatusDone, stored.Status)
	_, err = store.ClaimDue(ctx, now.Add(time.Hour), 10*time.Second)
	require.ErrorIs(t, err, service.ErrVideoStudioQueueEmpty)

	err = store.CommitClaim(ctx, first, time.Hour, nil)
	require.True(t, errors.Is(err, service.ErrVideoStudioTaskClaimLost))
}

func TestVideoStudioTaskStoreTransientClaimReadFailureKeepsTaskDue(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := &videoStudioTaskStore{rdb: client}
	ctx := context.Background()
	now := time.Now().UTC()
	task := &service.VideoStudioTask{
		RequestID: "request-retry", UserID: 1, APIKeyID: 2,
		Status: service.VideoStudioTaskStatusPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}

	require.NoError(t, store.Enqueue(ctx, task, time.Hour, now.Add(-time.Second)))
	client.AddHook(newVideoStudioFailNextGetHook())

	_, err := store.ClaimDue(ctx, now, 10*time.Second)
	require.EqualError(t, err, "temporary redis read failure")

	mr.FastForward(11 * time.Second)
	recoveryClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = recoveryClient.Close() })
	recoveryStore := &videoStudioTaskStore{rdb: recoveryClient}
	claim, err := recoveryStore.ClaimDue(ctx, now.Add(11*time.Second), 10*time.Second)
	require.NoError(t, err)
	require.Equal(t, task.RequestID, claim.Task.RequestID)
}

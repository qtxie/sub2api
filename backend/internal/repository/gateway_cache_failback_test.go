package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheOpenAIFailbackStateAtomicSequence(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &gatewayCache{rdb: rdb}
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)

	state, err := cache.RecordOpenAIFailbackFailure(ctx, 41, now, 5*time.Minute, 5*time.Minute, 30*time.Minute, 5*time.Minute, time.Hour)
	require.NoError(t, err)
	require.True(t, state.CooldownUntil.IsZero(), "an account that has not failed back must not enter adaptive cooldown")

	state, err = cache.RecordOpenAIFailback(ctx, 42, now, 5*time.Minute, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 300, state.CooldownSeconds)
	require.Equal(t, now, state.LastFailbackAt)

	state, err = cache.RecordOpenAIFailbackFailure(ctx, 42, now.Add(time.Minute), 5*time.Minute, 5*time.Minute, 30*time.Minute, 5*time.Minute, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 600, state.CooldownSeconds)
	firstCooldownUntil := state.CooldownUntil
	state, err = cache.RecordOpenAIFailbackFailure(ctx, 42, now.Add(90*time.Second), 5*time.Minute, 5*time.Minute, 30*time.Minute, 5*time.Minute, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 600, state.CooldownSeconds)
	require.Equal(t, firstCooldownUntil, state.CooldownUntil, "the cleared failback marker must prevent a duplicate escalation")

	_, err = cache.RecordOpenAIFailback(ctx, 42, now.Add(2*time.Minute), 5*time.Minute, time.Hour)
	require.NoError(t, err)
	state, err = cache.RecordOpenAIFailbackFailure(ctx, 42, now.Add(3*time.Minute), 5*time.Minute, 5*time.Minute, 30*time.Minute, 5*time.Minute, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 900, state.CooldownSeconds)

	_, err = cache.RecordOpenAIFailback(ctx, 42, now.Add(4*time.Minute), 5*time.Minute, time.Hour)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		var reset bool
		state, reset, err = cache.RecordOpenAIFailbackProductionResult(ctx, 42, now.Add(time.Duration(5+i)*time.Minute), true, 3, 5*time.Minute, time.Hour)
		require.NoError(t, err)
		require.Equal(t, i == 2, reset)
	}
	require.Equal(t, 300, state.CooldownSeconds)
	require.True(t, state.LastFailbackAt.IsZero())
}

func TestGatewayCacheReconcileOpenAIFailbackStatePreservesConservativeState(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &gatewayCache{rdb: rdb}
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)

	local := service.OpenAIFailbackState{
		CooldownSeconds: 600,
		CooldownUntil:   now.Add(10 * time.Minute),
		LastFailbackAt:  now,
		FastCount:       2,
	}
	state, err := cache.ReconcileOpenAIFailbackState(ctx, 51, local, time.Hour)
	require.NoError(t, err)
	require.Equal(t, local, state)

	remoteLast := now.Add(time.Minute)
	err = rdb.HSet(ctx, openAIFailbackStateKey(51),
		"cooldown_seconds", 900,
		"cooldown_until_ms", now.Add(15*time.Minute).UnixMilli(),
		"last_failback_ms", remoteLast.UnixMilli(),
		"fast_count", 1,
	).Err()
	require.NoError(t, err)

	state, err = cache.ReconcileOpenAIFailbackState(ctx, 51, local, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 900, state.CooldownSeconds)
	require.Equal(t, now.Add(15*time.Minute), state.CooldownUntil)
	require.Equal(t, remoteLast, state.LastFailbackAt)
	require.Equal(t, 1, state.FastCount)
}

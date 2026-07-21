package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIFailbackStoreCASAndProbeLease(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &gatewayCache{rdb: client}
	ctx := context.Background()

	value, found, err := cache.GetOpenAIFailbackState(ctx, "account-model")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, value)

	swapped, err := cache.CompareAndSwapOpenAIFailbackState(ctx, "account-model", "", `{"phase":"cooldown"}`, time.Hour)
	require.NoError(t, err)
	require.True(t, swapped)

	swapped, err = cache.CompareAndSwapOpenAIFailbackState(ctx, "account-model", "stale", `{"phase":"probation"}`, time.Hour)
	require.NoError(t, err)
	require.False(t, swapped)

	swapped, err = cache.CompareAndSwapOpenAIFailbackState(ctx, "account-model", `{"phase":"cooldown"}`, `{"phase":"probation"}`, time.Hour)
	require.NoError(t, err)
	require.True(t, swapped)

	acquired, err := cache.AcquireOpenAIFailbackProbe(ctx, "account-model", "owner-1", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = cache.AcquireOpenAIFailbackProbe(ctx, "account-model", "owner-2", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)

	require.NoError(t, cache.ReleaseOpenAIFailbackProbe(ctx, "account-model", "owner-2"))
	acquired, err = cache.AcquireOpenAIFailbackProbe(ctx, "account-model", "owner-2", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)
	require.NoError(t, cache.ReleaseOpenAIFailbackProbe(ctx, "account-model", "owner-1"))
	acquired, err = cache.AcquireOpenAIFailbackProbe(ctx, "account-model", "owner-2", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
}

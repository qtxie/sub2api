//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newAPIKeyRotationCache(t *testing.T) *gatewayCache {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &gatewayCache{rdb: rdb}
}

func TestGatewayCacheOpenAIAPIKeyRotation(t *testing.T) {
	ctx := context.Background()
	cache := newAPIKeyRotationCache(t)
	const accountID int64 = 42
	const fingerprint = "pool-v1"

	selection, err := cache.ResolveOpenAIAPIKeyIndex(ctx, accountID, fingerprint, 3)
	require.NoError(t, err)
	require.Zero(t, selection.ActiveIndex)
	require.Zero(t, selection.Generation)

	for failure := 1; failure <= 2; failure++ {
		result, err := cache.RecordOpenAIAPIKeyStreamFailure(ctx, accountID, fingerprint, 0, 0, 3, 3)
		require.NoError(t, err)
		require.Equal(t, 0, result.ActiveIndex)
		require.Equal(t, failure, result.FailureCount)
		require.False(t, result.Rotated)
		require.True(t, result.Recorded)
	}

	result, err := cache.RecordOpenAIAPIKeyStreamFailure(ctx, accountID, fingerprint, 0, 0, 3, 3)
	require.NoError(t, err)
	require.Equal(t, 1, result.ActiveIndex)
	require.Zero(t, result.FailureCount)
	require.True(t, result.Rotated)
	require.True(t, result.Recorded)
	require.Equal(t, int64(1), result.Generation)

	// A late failure from the old key must not count against the new active key.
	result, err = cache.RecordOpenAIAPIKeyStreamFailure(ctx, accountID, fingerprint, 0, 0, 3, 3)
	require.NoError(t, err)
	require.Equal(t, 1, result.ActiveIndex)
	require.Zero(t, result.FailureCount)
	require.False(t, result.Rotated)
	require.False(t, result.Recorded)

	result, err = cache.RecordOpenAIAPIKeyStreamFailure(ctx, accountID, fingerprint, 1, 1, 3, 3)
	require.NoError(t, err)
	require.Equal(t, 1, result.FailureCount)
	previous, reset, err := cache.ResetOpenAIAPIKeyStreamFailures(ctx, accountID, fingerprint, 1, 1)
	require.NoError(t, err)
	require.True(t, reset)
	require.Equal(t, 1, previous)

	// Editing the configured pool changes its fingerprint and returns to default.
	selection, err = cache.ResolveOpenAIAPIKeyIndex(ctx, accountID, "pool-v2", 2)
	require.NoError(t, err)
	require.Zero(t, selection.ActiveIndex)
	require.Zero(t, selection.Generation)

	// An old in-flight request cannot restore the previous pool fingerprint.
	stale, err := cache.RecordOpenAIAPIKeyStreamFailure(ctx, accountID, fingerprint, 1, 1, 3, 3)
	require.NoError(t, err)
	require.False(t, stale.Recorded)
	selection, err = cache.ResolveOpenAIAPIKeyIndex(ctx, accountID, "pool-v2", 2)
	require.NoError(t, err)
	require.Zero(t, selection.ActiveIndex)
}

func TestGatewayCacheOpenAIAPIKeyRotationCyclesThroughPool(t *testing.T) {
	ctx := context.Background()
	cache := newAPIKeyRotationCache(t)
	const accountID int64 = 43
	const fingerprint = "pool"

	for attempted := 0; attempted < 3; attempted++ {
		selection, err := cache.ResolveOpenAIAPIKeyIndex(ctx, accountID, fingerprint, 3)
		require.NoError(t, err)
		require.Equal(t, attempted, selection.ActiveIndex)
		for range 3 {
			_, err = cache.RecordOpenAIAPIKeyStreamFailure(ctx, accountID, fingerprint, attempted, selection.Generation, 3, 3)
			require.NoError(t, err)
		}
	}
	selection, err := cache.ResolveOpenAIAPIKeyIndex(ctx, accountID, fingerprint, 3)
	require.NoError(t, err)
	require.Zero(t, selection.ActiveIndex)
	require.Equal(t, int64(3), selection.Generation)

	// The index is back at zero, but a request from generation zero is stale.
	stale, err := cache.RecordOpenAIAPIKeyStreamFailure(ctx, accountID, fingerprint, 0, 0, 3, 3)
	require.NoError(t, err)
	require.False(t, stale.Recorded)
}

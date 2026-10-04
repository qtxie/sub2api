package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testOpenAIFailbackConfigWithSlowTripCount(tripCount int) openAIFailbackConfig {
	cfg := testOpenAIFailbackConfig()
	cfg.productionSlowTripCount = tripCount
	return cfg
}

func TestOpenAIFailbackConfigSlowTripCountFallsBackToDefault(t *testing.T) {
	require.Equal(t, openAIFailbackProductionSlowTripCount, openAIFailbackConfig{}.slowTripCount())
	require.Equal(t, 1, openAIFailbackConfig{productionSlowTripCount: 1}.slowTripCount())
	require.Equal(t, 7, openAIFailbackConfig{productionSlowTripCount: 7}.slowTripCount())
	// 非法值回落到常量默认，避免配置为 0 时永远无法冷却。
	require.Equal(t, openAIFailbackProductionSlowTripCount, openAIFailbackConfig{productionSlowTripCount: -1}.slowTripCount())
}

// tripCount=1 的语义是"首个慢样本立刻冷却"。若只在 SlowObservation 阶段计数，
// 首次写入状态不会触发冷却、要等第二个慢样本，与配置语义不符。
func TestOpenAIFailbackSlowTripCountOneTripsOnFirstSample(t *testing.T) {
	ctx := context.Background()
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfigWithSlowTripCount(1))

	slowTTFT := 30_001
	controller.recordProductionResult(ctx, 9, "gpt-5-mini", true, &slowTTFT)

	state := requireOpenAIFailbackState(t, controller, 9, "gpt-5-mini")
	require.Equal(t, openAIFailbackPhaseCooldown, state.Phase)
	require.Equal(t, openAIFailbackProductionSlow, state.LastFailure)
	require.True(t, controller.shouldFailOpenSlow(ctx, 9, "gpt-5-mini"))
	require.EqualValues(t, 1, controller.snapshotMetrics().ProductionSlow)

	// 恰好等于阈值不算慢（严格大于）。
	controller2 := newOpenAIFailbackController(nil, testOpenAIFailbackConfigWithSlowTripCount(1))
	atThreshold := 30_000
	controller2.recordProductionResult(ctx, 11, "gpt-5-mini", true, &atThreshold)
	key, ok := openAIFailbackStateKey(11, "gpt-5-mini")
	require.True(t, ok)
	_, found := controller2.readState(ctx, key)
	require.False(t, found)
}

// 默认 tripCount（3）必须保持历史行为：前两次只观察，第三次才冷却。
func TestOpenAIFailbackSlowTripCountDefaultPreservesHistory(t *testing.T) {
	ctx := context.Background()
	controller := newOpenAIFailbackController(nil, testOpenAIFailbackConfig())

	recordOpenAIFailbackSlowProductionResults(ctx, controller, 9, "gpt-5-mini", 2)
	state := requireOpenAIFailbackState(t, controller, 9, "gpt-5-mini")
	require.Equal(t, openAIFailbackPhaseSlowObservation, state.Phase)
	require.Equal(t, 2, state.ConsecutiveSlowTTFT)

	recordOpenAIFailbackSlowProductionResults(ctx, controller, 9, "gpt-5-mini", 1)
	state = requireOpenAIFailbackState(t, controller, 9, "gpt-5-mini")
	require.Equal(t, openAIFailbackPhaseCooldown, state.Phase)
	require.Equal(t, openAIFailbackProductionSlow, state.LastFailure)
}

// 更高的阈值（50s）下，40s 的样本应视为健康。
func TestOpenAIFailbackProductionSlowTTFTThresholdIsConfigurable(t *testing.T) {
	ctx := context.Background()
	cfg := testOpenAIFailbackConfigWithSlowTripCount(1)
	cfg.productionSlowTTFT = 50 * time.Second
	controller := newOpenAIFailbackController(nil, cfg)

	healthy := 40_000
	controller.recordProductionResult(ctx, 9, "gpt-5-mini", true, &healthy)
	key, ok := openAIFailbackStateKey(9, "gpt-5-mini")
	require.True(t, ok)
	_, found := controller.readState(ctx, key)
	require.False(t, found, "40s must be healthy under a 50s threshold")

	slow := 50_001
	controller.recordProductionResult(ctx, 9, "gpt-5-mini", true, &slow)
	state := requireOpenAIFailbackState(t, controller, 9, "gpt-5-mini")
	require.Equal(t, openAIFailbackPhaseCooldown, state.Phase)
	require.Equal(t, openAIFailbackProductionSlow, state.LastFailure)
}

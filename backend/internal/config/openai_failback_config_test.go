package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadOpenAIFailbackDefaults(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)

	failback := cfg.Gateway.OpenAIScheduler
	require.True(t, failback.FailbackProbeEnabled)
	require.Equal(t, 120, failback.FailbackDefaultCooldownSeconds)
	require.Equal(t, 180, failback.FailbackCooldownIncrementSeconds)
	require.Equal(t, 1560, failback.FailbackCooldownMaxSeconds)
	require.Equal(t, 300, failback.FailbackProbationSeconds)
	require.Equal(t, 20, failback.FailbackProbeTimeoutSeconds)
	require.Equal(t, 20_000, failback.FailbackMaxTTFTMs)
	require.Equal(t, 3, failback.FailbackMinHealthyRequests)
}

func TestValidateOpenAIFailbackConfiguration(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)

	cfg.Gateway.OpenAIScheduler.FailbackCooldownMaxSeconds = 60
	require.ErrorContains(t, cfg.Validate(), "max cooldown")

	cfg.Gateway.OpenAIScheduler.FailbackProbeEnabled = false
	require.NoError(t, cfg.Validate())
}

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadOpenAIRequestRetryDefaults(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)

	retry := cfg.Gateway.OpenAIRequestRetry
	require.True(t, retry.Enabled)
	require.Equal(t, 1800, retry.TotalBudgetSeconds)
	require.Zero(t, retry.MaxAttempts)
	require.Equal(t, 1000, retry.MaxWaitingRequests)
	require.Equal(t, 2, retry.BackoffInitialSeconds)
	require.Equal(t, 30, retry.BackoffMaxSeconds)
	require.InDelta(t, 0.2, retry.JitterRatio, 0.0001)
	require.True(t, retry.WaitForTemporaryCapacity)
}

func TestValidateOpenAIRequestRetryConfiguration(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)

	cfg.Gateway.OpenAIRequestRetry.MaxAttempts = -1
	require.ErrorContains(t, cfg.Validate(), "max_attempts")

	cfg.Gateway.OpenAIRequestRetry.MaxAttempts = 0
	cfg.Gateway.OpenAIRequestRetry.MaxWaitingRequests = -1
	require.ErrorContains(t, cfg.Validate(), "max_waiting_requests")

	cfg.Gateway.OpenAIRequestRetry.MaxWaitingRequests = 1000
	cfg.Gateway.OpenAIRequestRetry.BackoffMaxSeconds = 1
	require.ErrorContains(t, cfg.Validate(), "backoff_max_seconds")

	cfg.Gateway.OpenAIRequestRetry.Enabled = false
	require.NoError(t, cfg.Validate())
}

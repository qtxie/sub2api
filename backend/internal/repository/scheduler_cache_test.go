package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFilterSchedulerCredentialsKeepsSubscriptionPlanType(t *testing.T) {
	filtered := filterSchedulerCredentials(map[string]any{
		"plan_type":     "plus",
		"access_token":  "secret-access-token",
		"refresh_token": "secret-refresh-token",
	})

	require.Equal(t, "plus", filtered["plan_type"])
	require.NotContains(t, filtered, "access_token")
	require.NotContains(t, filtered, "refresh_token")
}

func TestFilterSchedulerCredentialsKeepsPoolModeRetrySettings(t *testing.T) {
	filtered := filterSchedulerCredentials(map[string]any{
		"pool_mode":                    true,
		"pool_mode_retry_count":        float64(5),
		"pool_mode_retry_status_codes": []any{float64(502), float64(503), float64(504)},
		"access_token":                 "secret-access-token",
		"refresh_token":                "secret-refresh-token",
	})

	require.Equal(t, true, filtered["pool_mode"])
	require.Equal(t, float64(5), filtered["pool_mode_retry_count"])
	require.Equal(t, []any{float64(502), float64(503), float64(504)}, filtered["pool_mode_retry_status_codes"])
	require.NotContains(t, filtered, "access_token")
	require.NotContains(t, filtered, "refresh_token")
}

func TestSchedulerMetadataAccountKeepsOpenAISubscriptionIdentity(t *testing.T) {
	account := service.Account{
		ID:       24,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"plan_type":    "plus",
			"access_token": "secret-access-token",
		},
	}

	metadata := buildSchedulerMetadataAccount(account)

	require.True(t, metadata.IsOpenAIChatGPTSubscription())
	require.Empty(t, metadata.GetCredential("access_token"))
}

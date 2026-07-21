package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestLoadTelegramNotificationFromEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("NOTIFICATIONS_TELEGRAM_ENABLED", "true")
	t.Setenv("NOTIFICATIONS_TELEGRAM_BOT_TOKEN", "123456:bot-token")
	t.Setenv("NOTIFICATIONS_TELEGRAM_CHAT_ID", "-1001234567890")
	t.Setenv("NOTIFICATIONS_TELEGRAM_THREAD_ID", "42")
	t.Setenv("NOTIFICATIONS_TELEGRAM_NOTIFY_ERRORS", "false")
	t.Setenv("NOTIFICATIONS_TELEGRAM_NOTIFY_TIMEOUTS", "true")
	t.Setenv("NOTIFICATIONS_TELEGRAM_NOTIFY_ACCOUNT_SWITCHES", "true")
	t.Setenv("NOTIFICATIONS_TELEGRAM_DEDUPE_WINDOW_SECONDS", "600")
	t.Setenv("NOTIFICATIONS_TELEGRAM_QUEUE_SIZE", "256")
	t.Setenv("NOTIFICATIONS_TELEGRAM_REQUEST_TIMEOUT_SECONDS", "15")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, TelegramNotificationConfig{
		Enabled:               true,
		BotToken:              "123456:bot-token",
		ChatID:                "-1001234567890",
		ThreadID:              42,
		NotifyErrors:          false,
		NotifyTimeouts:        true,
		NotifyAccountSwitches: true,
		DedupeWindowSeconds:   600,
		QueueSize:             256,
		RequestTimeoutSeconds: 15,
	}, cfg.Notifications.Telegram)

	// Ensure this test does not leave environment-backed Viper state for tests
	// that run in the same package.
	viper.Reset()
}

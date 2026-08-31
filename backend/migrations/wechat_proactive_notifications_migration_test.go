package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWeChatProactiveNotificationsAreOptIn(t *testing.T) {
	content, err := FS.ReadFile("234_disable_wechat_proactive_notifications.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER COLUMN notify_admin SET DEFAULT FALSE")
	require.Contains(t, sql, "ALTER COLUMN notify_balance SET DEFAULT FALSE")
	require.Contains(t, sql, "ALTER COLUMN notify_login SET DEFAULT FALSE")
	require.Contains(t, sql, "SET notify_admin = FALSE")
	require.Contains(t, sql, "notify_balance = FALSE")
	require.Contains(t, sql, "notify_login = FALSE")
	require.Contains(t, sql, "status = 'cancelled'")
	require.Contains(t, sql, "WHERE status IN ('pending', 'blocked', 'retry', 'sending')")
}

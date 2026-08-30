package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWeChatILinkPerUserMigrationDoesNotReuseSharedCredentials(t *testing.T) {
	content, err := FS.ReadFile("233_wechat_ilink_per_user.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE wechat_bot_accounts")
	require.Contains(t, sql, "user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE")
	require.Contains(t, sql, "bot_token_encrypted TEXT NOT NULL DEFAULT ''")
	require.Contains(t, sql, "context_token_encrypted TEXT NOT NULL DEFAULT ''")
	require.Contains(t, sql, "poller_lease_owner TEXT NOT NULL DEFAULT ''")
	require.Contains(t, sql, "REFERENCES wechat_bot_accounts(user_id) ON DELETE CASCADE")

	insertStart := strings.Index(sql, "INSERT INTO wechat_bot_accounts")
	require.NotEqual(t, -1, insertStart)
	insertEnd := strings.Index(sql[insertStart:], "ON CONFLICT (user_id) DO NOTHING")
	require.NotEqual(t, -1, insertEnd)
	settingsCopy := sql[insertStart : insertStart+insertEnd]
	require.Contains(t, settingsCopy, "notify_admin")
	require.Contains(t, settingsCopy, "chat_model")
	require.NotContains(t, settingsCopy, "bot_token_encrypted")
	require.NotContains(t, settingsCopy, "bot_id")
	require.NotContains(t, settingsCopy, "ilink_user_id")
	require.NotContains(t, settingsCopy, "context_token_encrypted")
	require.NotContains(t, settingsCopy, "get_updates_buf")

	require.Contains(t, sql, "DROP TABLE wechat_bot_state")
	require.Contains(t, sql, "DROP TABLE wechat_bot_bindings")
	require.Contains(t, sql, "DROP TABLE wechat_bot_outbox")
}

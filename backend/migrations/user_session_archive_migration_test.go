package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserSessionArchiveMigrationsArePrivateAndUserScoped(t *testing.T) {
	flag, err := FS.ReadFile("196_add_user_session_storage_flag.sql")
	require.NoError(t, err)
	require.Contains(t, string(flag), "session_storage_enabled BOOLEAN NOT NULL DEFAULT FALSE")

	content, err := FS.ReadFile("197_user_session_archive.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, table := range []string{"user_sessions", "user_session_requests", "user_session_turns", "user_session_parts", "user_session_blobs"} {
		require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS "+table)
	}
	require.Contains(t, sql, "UNIQUE (user_id, kind, content_sha256, byte_length)")
	require.Contains(t, sql, "inline_bytes BYTEA")
	require.Contains(t, sql, "object_key TEXT")
	require.Contains(t, sql, "active_head_turn_id")
	require.NotContains(t, strings.ToLower(sql), "prompt_audit")
}

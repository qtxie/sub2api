package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestTelegramNotificationOutboxRepositoryEnqueueUsesAtomicDedupeUpsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	availableAt := time.Now().UTC().Add(5 * time.Second)
	mock.ExpectExec("INSERT INTO telegram_notification_outbox").
		WithArgs(strings.Repeat("a", 64), int64(42), "error", sqlmock.AnyArg(), availableAt).
		WillReturnResult(sqlmock.NewResult(1, 1))

	repo := NewTelegramNotificationOutboxRepository(db)
	err = repo.Enqueue(context.Background(), service.GatewayNotificationEvent{
		Type:       service.GatewayNotificationEventError,
		Platform:   "openai",
		AccountID:  9,
		OccurredAt: time.Now().UTC(),
	}, strings.Repeat("a", 64), 42, availableAt)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTelegramNotificationOutboxRepositoryClaimUsesLeaseAndDecodesPayload(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	now := time.Now().UTC()
	mock.ExpectQuery("(?s)FROM telegram_notification_outbox.*FOR UPDATE SKIP LOCKED.*RETURNING").
		WithArgs("worker-a", 25, int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "event_type", "payload", "occurrence_count", "created_at", "last_occurred_at", "attempts",
		}).AddRow(
			int64(7),
			"switch",
			[]byte(`{"type":"switch","platform":"openai","account_id":9,"model":"gpt-5","occurred_at":"2026-01-01T00:00:00Z"}`),
			3,
			now,
			now,
			2,
		))

	repo := NewTelegramNotificationOutboxRepository(db)
	events, err := repo.Claim(context.Background(), "worker-a", 0, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, int64(7), events[0].ID)
	require.Equal(t, service.GatewayNotificationEventSwitch, events[0].Event.Type)
	require.Equal(t, int64(9), events[0].Event.AccountID)
	require.Equal(t, 3, events[0].OccurrenceCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTelegramNotificationOutboxRepositoryRequiresClaimOwnership(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewTelegramNotificationOutboxRepository(db)
	mock.ExpectExec("UPDATE telegram_notification_outbox").
		WithArgs(int64(7), "old-worker").
		WillReturnResult(sqlmock.NewResult(0, 0))
	err = repo.MarkDelivered(context.Background(), 7, "old-worker")
	require.ErrorContains(t, err, "no longer owned")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTelegramNotificationMigrationProtectsDedupeAndAvoidsSecrets(t *testing.T) {
	content, err := migrations.FS.ReadFile("185_telegram_notification_outbox.sql")
	require.NoError(t, err)
	sqlText := string(content)
	for _, required := range []string{
		"telegram_notification_outbox",
		"UNIQUE (dedupe_key, dedupe_bucket)",
		"claimed_at",
	} {
		require.Contains(t, sqlText, required)
	}
	require.Regexp(t, regexp.MustCompile(`dedupe_key\s+CHAR\(64\)`), sqlText)
	require.Regexp(t, regexp.MustCompile(`dedupe_bucket\s+BIGINT`), sqlText)
	require.NotContains(t, strings.ToLower(sqlText), "bot_token")
	require.NotContains(t, strings.ToLower(sqlText), "api_key")
}

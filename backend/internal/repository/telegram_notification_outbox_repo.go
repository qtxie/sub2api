package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const telegramNotificationOutboxDefaultClaimLimit = 25

type telegramNotificationOutboxRepository struct {
	db *sql.DB
}

func NewTelegramNotificationOutboxRepository(db *sql.DB) service.TelegramNotificationOutboxRepository {
	return &telegramNotificationOutboxRepository{db: db}
}

func (r *telegramNotificationOutboxRepository) Enqueue(ctx context.Context, event service.GatewayNotificationEvent, dedupeKey string, dedupeBucket int64, availableAt time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("nil telegram notification outbox database")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal telegram notification event: %w", err)
	}
	if availableAt.IsZero() {
		availableAt = time.Now().UTC()
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO telegram_notification_outbox
			(dedupe_key, dedupe_bucket, event_type, payload, available_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (dedupe_key, dedupe_bucket) DO UPDATE
		SET occurrence_count = telegram_notification_outbox.occurrence_count + 1,
			last_occurred_at = NOW(),
			payload = EXCLUDED.payload
	`, dedupeKey, dedupeBucket, string(event.Type), payload, availableAt.UTC())
	if err != nil {
		return fmt.Errorf("enqueue telegram notification: %w", err)
	}
	return nil
}

func (r *telegramNotificationOutboxRepository) Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]service.TelegramNotificationOutboxEvent, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil telegram notification outbox database")
	}
	if limit <= 0 {
		limit = telegramNotificationOutboxDefaultClaimLimit
	}
	leaseSeconds := int64(lease / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 30
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM telegram_notification_outbox
			WHERE delivered_at IS NULL
			  AND available_at <= NOW()
			  AND (claimed_at IS NULL OR claimed_at < NOW() - ($3 * INTERVAL '1 second'))
			ORDER BY id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE telegram_notification_outbox AS o
		SET claimed_at = NOW(), claimed_by = $1
		FROM candidates AS c
		WHERE o.id = c.id
		RETURNING o.id, o.event_type, o.payload, o.occurrence_count, o.created_at, o.last_occurred_at, o.attempts
	`, workerID, limit, leaseSeconds)
	if err != nil {
		return nil, fmt.Errorf("claim telegram notifications: %w", err)
	}
	defer func() { _ = rows.Close() }()

	events := make([]service.TelegramNotificationOutboxEvent, 0, limit)
	for rows.Next() {
		var (
			event      service.TelegramNotificationOutboxEvent
			payloadRaw []byte
		)
		if err := rows.Scan(
			&event.ID,
			&event.Event.Type,
			&payloadRaw,
			&event.OccurrenceCount,
			&event.CreatedAt,
			&event.LastOccurredAt,
			&event.Attempts,
		); err != nil {
			return nil, fmt.Errorf("scan telegram notification: %w", err)
		}
		if err := json.Unmarshal(payloadRaw, &event.Event); err != nil {
			return nil, fmt.Errorf("decode telegram notification %d: %w", event.ID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (r *telegramNotificationOutboxRepository) MarkDelivered(ctx context.Context, id int64, workerID string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE telegram_notification_outbox
		SET delivered_at = NOW(), claimed_at = NULL, claimed_by = NULL, last_error = NULL
		WHERE id = $1 AND claimed_by = $2 AND delivered_at IS NULL
	`, id, workerID)
	if err != nil {
		return err
	}
	return requireTelegramNotificationClaim(result, id, workerID)
}

func (r *telegramNotificationOutboxRepository) RetryClaimed(ctx context.Context, id int64, workerID string, availableAt time.Time, lastError string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE telegram_notification_outbox
		SET attempts = attempts + 1,
			available_at = $3,
			last_error = $4,
			claimed_at = NULL,
			claimed_by = NULL
		WHERE id = $1 AND claimed_by = $2 AND delivered_at IS NULL
	`, id, workerID, availableAt.UTC(), lastError)
	if err != nil {
		return err
	}
	return requireTelegramNotificationClaim(result, id, workerID)
}

func (r *telegramNotificationOutboxRepository) DeleteDeliveredBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("nil telegram notification outbox database")
	}
	if limit <= 0 {
		limit = 500
	}
	result, err := r.db.ExecContext(ctx, `
		WITH doomed AS (
			SELECT id
			FROM telegram_notification_outbox
			WHERE delivered_at IS NOT NULL AND delivered_at < $1
			ORDER BY id ASC
			LIMIT $2
		)
		DELETE FROM telegram_notification_outbox AS o
		USING doomed AS d
		WHERE o.id = d.id
	`, before.UTC(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func requireTelegramNotificationClaim(result sql.Result, id int64, workerID string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("telegram notification claim %d is no longer owned by %s", id, workerID)
	}
	return nil
}

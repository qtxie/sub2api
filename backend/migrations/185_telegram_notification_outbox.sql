-- Durable Telegram delivery queue for operational gateway notifications.
-- The unique dedupe bucket ensures clustered instances emit one alert per
-- event fingerprint/window without putting Telegram I/O on the request path.

CREATE TABLE IF NOT EXISTS telegram_notification_outbox (
    id                BIGSERIAL PRIMARY KEY,
    dedupe_key        CHAR(64) NOT NULL CHECK (dedupe_key ~ '^[0-9a-f]{64}$'),
    dedupe_bucket     BIGINT NOT NULL,
    event_type        VARCHAR(32) NOT NULL,
    payload           JSONB NOT NULL,
    occurrence_count  INTEGER NOT NULL DEFAULT 1 CHECK (occurrence_count > 0),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    available_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts          INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error        TEXT,
    claimed_at        TIMESTAMPTZ,
    claimed_by        TEXT,
    delivered_at      TIMESTAMPTZ,
    CONSTRAINT telegram_notification_outbox_dedupe UNIQUE (dedupe_key, dedupe_bucket)
);

CREATE INDEX IF NOT EXISTS idx_telegram_notification_outbox_claimable
    ON telegram_notification_outbox (available_at, id)
    WHERE delivered_at IS NULL AND claimed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_telegram_notification_outbox_lease
    ON telegram_notification_outbox (claimed_at)
    WHERE delivered_at IS NULL AND claimed_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_telegram_notification_outbox_delivered
    ON telegram_notification_outbox (delivered_at)
    WHERE delivered_at IS NOT NULL;

COMMENT ON TABLE telegram_notification_outbox IS
    'Durable, deduplicated Telegram operational notifications; payload intentionally excludes request bodies and credentials';

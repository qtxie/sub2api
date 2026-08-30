-- Replace the shared iLink bot session with one independently authorized bot
-- account per Sub2API user. Shared credentials and their pending deliveries are
-- intentionally discarded because they cannot be attributed to a user-owned
-- ClawBot session safely.

CREATE TABLE wechat_bot_accounts (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    bot_token_encrypted TEXT NOT NULL DEFAULT '',
    base_url TEXT NOT NULL DEFAULT 'https://ilinkai.weixin.qq.com',
    bot_id TEXT NOT NULL DEFAULT '',
    ilink_user_id TEXT NOT NULL DEFAULT '',
    get_updates_buf TEXT NOT NULL DEFAULT '',
    context_token_encrypted TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    notify_admin BOOLEAN NOT NULL DEFAULT TRUE,
    notify_balance BOOLEAN NOT NULL DEFAULT TRUE,
    notify_login BOOLEAN NOT NULL DEFAULT TRUE,
    chat_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    chat_api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    chat_model VARCHAR(160) NOT NULL DEFAULT 'gpt-4o-mini',
    chat_history_encrypted TEXT NOT NULL DEFAULT '',
    last_inbound_at TIMESTAMPTZ,
    last_outbound_at TIMESTAMPTZ,
    outbound_count INTEGER NOT NULL DEFAULT 0 CHECK (outbound_count >= 0),
    poller_lease_owner TEXT NOT NULL DEFAULT '',
    poller_lease_until TIMESTAMPTZ,
    last_connected_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Preserve user choices while requiring every user to scan their own QR code.
INSERT INTO wechat_bot_accounts (
    user_id, enabled, notify_admin, notify_balance, notify_login,
    chat_enabled, chat_api_key_id, chat_model, chat_history_encrypted,
    created_at, updated_at
)
SELECT user_id, enabled, notify_admin, notify_balance, notify_login,
       chat_enabled, chat_api_key_id, chat_model, chat_history_encrypted,
       created_at, updated_at
FROM wechat_bot_bindings
ON CONFLICT (user_id) DO NOTHING;

CREATE UNIQUE INDEX idx_wechat_bot_accounts_bot_id
    ON wechat_bot_accounts(bot_id)
    WHERE bot_id <> '';

CREATE UNIQUE INDEX idx_wechat_bot_accounts_ilink_user_id
    ON wechat_bot_accounts(ilink_user_id)
    WHERE ilink_user_id <> '';

CREATE INDEX idx_wechat_bot_accounts_polling
    ON wechat_bot_accounts(poller_lease_until, user_id)
    WHERE bot_token_encrypted <> '';

CREATE INDEX idx_wechat_bot_accounts_delivery
    ON wechat_bot_accounts(enabled, last_inbound_at)
    WHERE bot_token_encrypted <> '';

DROP TABLE wechat_bot_outbox;
DROP TABLE wechat_bot_bindings;
DROP TABLE wechat_bot_state;

CREATE TABLE wechat_bot_outbox (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES wechat_bot_accounts(user_id) ON DELETE CASCADE,
    event_type VARCHAR(32) NOT NULL CHECK (event_type IN ('admin', 'balance', 'login')),
    message TEXT NOT NULL CHECK (message <> ''),
    status VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sending', 'retry', 'blocked', 'sent', 'cancelled')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_wechat_bot_outbox_pending
    ON wechat_bot_outbox(status, available_at, id)
    WHERE status IN ('pending', 'blocked', 'retry', 'sending');

CREATE INDEX idx_wechat_bot_outbox_cleanup
    ON wechat_bot_outbox(updated_at)
    WHERE status IN ('sent', 'cancelled');

-- WeChat iLink bot state, per-user bindings, and durable notification delivery.

CREATE TABLE IF NOT EXISTS wechat_bot_state (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    bot_token_encrypted TEXT NOT NULL DEFAULT '',
    base_url TEXT NOT NULL DEFAULT 'https://ilinkai.weixin.qq.com',
    bot_id TEXT NOT NULL DEFAULT '',
    ilink_user_id TEXT NOT NULL DEFAULT '',
    get_updates_buf TEXT NOT NULL DEFAULT '',
    poller_lease_owner TEXT NOT NULL DEFAULT '',
    poller_lease_until TIMESTAMPTZ,
    last_connected_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS wechat_bot_bindings (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    ilink_user_id TEXT UNIQUE,
    context_token_encrypted TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    notify_admin BOOLEAN NOT NULL DEFAULT TRUE,
    notify_balance BOOLEAN NOT NULL DEFAULT TRUE,
    notify_login BOOLEAN NOT NULL DEFAULT TRUE,
    chat_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    chat_api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    chat_model VARCHAR(160) NOT NULL DEFAULT 'gpt-4o-mini',
    chat_history_encrypted TEXT NOT NULL DEFAULT '',
    binding_code_hash CHAR(64),
    binding_code_expires_at TIMESTAMPTZ,
    last_inbound_at TIMESTAMPTZ,
    last_outbound_at TIMESTAMPTZ,
    outbound_count INTEGER NOT NULL DEFAULT 0 CHECK (outbound_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_wechat_bot_bindings_code
    ON wechat_bot_bindings(binding_code_hash)
    WHERE binding_code_hash IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_wechat_bot_bindings_delivery
    ON wechat_bot_bindings(enabled, last_inbound_at)
    WHERE ilink_user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS wechat_bot_outbox (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES wechat_bot_bindings(user_id) ON DELETE CASCADE,
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

CREATE INDEX IF NOT EXISTS idx_wechat_bot_outbox_pending
    ON wechat_bot_outbox(status, available_at, id)
    WHERE status IN ('pending', 'blocked', 'retry', 'sending');

CREATE INDEX IF NOT EXISTS idx_wechat_bot_outbox_cleanup
    ON wechat_bot_outbox(updated_at)
    WHERE status IN ('sent', 'cancelled');

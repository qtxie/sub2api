-- Proactive iLink notifications are opt-in for now. User replies and chat
-- responses remain available because they are initiated by the user.

ALTER TABLE wechat_bot_accounts
    ALTER COLUMN notify_admin SET DEFAULT FALSE,
    ALTER COLUMN notify_balance SET DEFAULT FALSE,
    ALTER COLUMN notify_login SET DEFAULT FALSE;

UPDATE wechat_bot_accounts
SET notify_admin = FALSE,
    notify_balance = FALSE,
    notify_login = FALSE,
    updated_at = NOW();

-- Do not retain notifications that were queued under the old default.
UPDATE wechat_bot_outbox
SET status = 'cancelled', lease_owner = '', lease_until = NULL,
    last_error = 'proactive notifications disabled by default', updated_at = NOW()
WHERE status IN ('pending', 'blocked', 'retry', 'sending');

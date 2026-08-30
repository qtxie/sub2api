package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type weChatBotRepository struct {
	db *sql.DB
}

func NewWeChatBotRepository(db *sql.DB) service.WeChatBotRepository {
	return &weChatBotRepository{db: db}
}

func (r *weChatBotRepository) GetAccount(ctx context.Context, userID int64) (*service.WeChatBotAccount, error) {
	return scanWeChatAccount(r.db.QueryRowContext(ctx, weChatAccountSelect+` WHERE user_id = $1`, userID))
}

func (r *weChatBotRepository) SaveLogin(ctx context.Context, userID int64, account *service.WeChatBotAccount) error {
	if userID <= 0 || account == nil {
		return errors.New("wechat bot account is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, identity := range []string{"bot:" + account.BotID, "user:" + account.ILinkUserID} {
		if strings.HasSuffix(identity, ":") {
			continue
		}
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, identity); err != nil {
			return err
		}
	}
	var ownerID int64
	err = tx.QueryRowContext(ctx, `
		SELECT user_id FROM wechat_bot_accounts
		WHERE user_id <> $1
		  AND (($2 <> '' AND bot_id = $2) OR ($3 <> '' AND ilink_user_id = $3))
		LIMIT 1 FOR UPDATE`, userID, account.BotID, account.ILinkUserID).Scan(&ownerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		return service.ErrWeChatBotIdentityBound
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO wechat_bot_accounts (
			user_id, bot_token_encrypted, base_url, bot_id, ilink_user_id,
			get_updates_buf, last_connected_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, '', NOW(), NOW(), NOW())
		ON CONFLICT (user_id) DO UPDATE SET
			bot_token_encrypted = EXCLUDED.bot_token_encrypted,
			base_url = EXCLUDED.base_url,
			bot_id = EXCLUDED.bot_id,
			ilink_user_id = EXCLUDED.ilink_user_id,
			get_updates_buf = '', context_token_encrypted = '',
			chat_history_encrypted = '', last_inbound_at = NULL,
			last_outbound_at = NULL, outbound_count = 0,
			poller_lease_owner = '', poller_lease_until = NULL,
			last_connected_at = NOW(), last_error = '', updated_at = NOW()`,
		userID, account.BotTokenEncrypted, account.BaseURL, account.BotID, account.ILinkUserID)
	if err != nil {
		if isUniqueViolation(err) {
			return service.ErrWeChatBotIdentityBound
		}
		return err
	}
	return tx.Commit()
}

func (r *weChatBotRepository) ClearLogin(ctx context.Context, userID int64, lastError string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_accounts SET
			bot_token_encrypted = '', get_updates_buf = '', context_token_encrypted = '',
			poller_lease_owner = '', poller_lease_until = NULL,
			last_error = $1, updated_at = NOW()
		WHERE user_id = $2`, boundedWeChatRepoError(lastError), userID)
	return err
}

func (r *weChatBotRepository) DeleteAccount(ctx context.Context, userID int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM wechat_bot_accounts WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return service.ErrWeChatBotAccountMissing
	}
	return err
}

func (r *weChatBotRepository) ListLoggedInUserIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id FROM wechat_bot_accounts
		WHERE bot_token_encrypted <> ''
		ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *weChatBotRepository) UpdateCursor(ctx context.Context, userID int64, cursor string, connectedAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_accounts
		SET get_updates_buf = $1, last_connected_at = $2, last_error = '', updated_at = NOW()
		WHERE user_id = $3`, cursor, connectedAt, userID)
	return err
}

func (r *weChatBotRepository) SetAccountError(ctx context.Context, userID int64, lastError string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_accounts SET last_error = $1, updated_at = NOW()
		WHERE user_id = $2`, boundedWeChatRepoError(lastError), userID)
	return err
}

func (r *weChatBotRepository) AcquirePollerLease(ctx context.Context, userID int64, owner string, lease time.Duration) (bool, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_accounts
		SET poller_lease_owner = $1,
			poller_lease_until = NOW() + ($2 * INTERVAL '1 millisecond'), updated_at = NOW()
		WHERE user_id = $3 AND bot_token_encrypted <> ''
		  AND (poller_lease_until IS NULL OR poller_lease_until < NOW() OR poller_lease_owner = $1)`,
		owner, lease.Milliseconds(), userID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (r *weChatBotRepository) UpdateAccountSettings(ctx context.Context, userID int64, update service.WeChatBotSettingsUpdate) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE wechat_bot_accounts SET
			enabled = $1, notify_admin = $2, notify_balance = $3, notify_login = $4,
			chat_enabled = $5, chat_api_key_id = $6, chat_model = $7, updated_at = NOW()
		WHERE user_id = $8`, update.Enabled, update.NotifyAdmin, update.NotifyBalance,
		update.NotifyLogin, update.ChatEnabled, update.ChatAPIKeyID, update.ChatModel, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return service.ErrWeChatBotAccountMissing
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE wechat_bot_outbox
		SET status = 'cancelled', lease_owner = '', lease_until = NULL,
			last_error = '', updated_at = NOW()
		WHERE user_id = $1 AND status IN ('pending', 'blocked', 'retry', 'sending')
		  AND (
			NOT $2 OR CASE event_type
				WHEN 'admin' THEN NOT $3
				WHEN 'balance' THEN NOT $4
				WHEN 'login' THEN NOT $5
				ELSE TRUE
			END
		  )`, userID, update.Enabled, update.NotifyAdmin, update.NotifyBalance, update.NotifyLogin)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *weChatBotRepository) TouchInbound(ctx context.Context, userID int64, ilinkUserID, encryptedContextToken string, inboundAt time.Time) (*service.WeChatBotAccount, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_accounts SET
			ilink_user_id = CASE WHEN ilink_user_id = '' THEN $1 ELSE ilink_user_id END,
			context_token_encrypted = CASE WHEN $2 = '' THEN context_token_encrypted ELSE $2 END,
			last_inbound_at = $3, outbound_count = 0, updated_at = NOW()
		WHERE user_id = $4 AND bot_token_encrypted <> ''
		  AND (ilink_user_id = '' OR ilink_user_id = $1)`,
		ilinkUserID, encryptedContextToken, inboundAt, userID)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, service.ErrWeChatBotAccountMissing
	}
	account, err := r.GetAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	_, _ = r.ReleasePendingForUser(ctx, userID)
	return account, nil
}

func (r *weChatBotRepository) IncrementOutbound(ctx context.Context, userID int64, sentAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_accounts
		SET outbound_count = outbound_count + 1, last_outbound_at = $1, updated_at = NOW()
		WHERE user_id = $2`, sentAt, userID)
	return err
}

func (r *weChatBotRepository) SaveChatHistory(ctx context.Context, userID int64, encryptedHistory string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_accounts SET chat_history_encrypted = $1, updated_at = NOW()
		WHERE user_id = $2`, encryptedHistory, userID)
	return err
}

func (r *weChatBotRepository) ClearChatHistory(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_accounts SET chat_history_encrypted = '', updated_at = NOW()
		WHERE user_id = $1`, userID)
	return err
}

func (r *weChatBotRepository) EnqueueNotification(ctx context.Context, userID int64, eventType, message string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO wechat_bot_outbox (user_id, event_type, message, status, available_at, created_at, updated_at)
		SELECT user_id, $2, $3, 'pending', NOW(), NOW(), NOW()
		FROM wechat_bot_accounts
		WHERE user_id = $1 AND bot_token_encrypted <> '' AND enabled
		  AND CASE $2
			WHEN 'admin' THEN notify_admin
			WHEN 'balance' THEN notify_balance
			WHEN 'login' THEN notify_login
			ELSE FALSE
		  END`, userID, eventType, message)
	return err
}

func (r *weChatBotRepository) EnqueueAdminBroadcast(ctx context.Context, userIDs []int64, message string) (int64, error) {
	query := `
		INSERT INTO wechat_bot_outbox (user_id, event_type, message, status, available_at, created_at, updated_at)
		SELECT user_id, 'admin', $1, 'pending', NOW(), NOW(), NOW()
		FROM wechat_bot_accounts
		WHERE bot_token_encrypted <> '' AND enabled AND notify_admin`
	args := []any{message}
	if len(userIDs) > 0 {
		query += ` AND user_id = ANY($2)`
		args = append(args, pq.Array(userIDs))
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *weChatBotRepository) ClaimOutbox(ctx context.Context, owner string, limit int, lease time.Duration) ([]service.WeChatBotOutboxItem, error) {
	if limit <= 0 {
		limit = 20
	}
	if lease <= 0 {
		lease = 45 * time.Second
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT o.id
			FROM wechat_bot_outbox o
			JOIN wechat_bot_accounts a ON a.user_id = o.user_id
			WHERE (
				o.status IN ('pending', 'blocked', 'retry')
				OR (o.status = 'sending' AND o.lease_until < NOW())
			)
			  AND o.available_at <= NOW()
			  AND a.bot_token_encrypted <> '' AND a.enabled
			  AND CASE o.event_type
				WHEN 'admin' THEN a.notify_admin
				WHEN 'balance' THEN a.notify_balance
				WHEN 'login' THEN a.notify_login
				ELSE FALSE
			  END
			  AND a.last_inbound_at >= NOW() - INTERVAL '24 hours'
			  AND a.outbound_count < 10
			ORDER BY o.id
			FOR UPDATE OF o SKIP LOCKED
			LIMIT $1
		)
		UPDATE wechat_bot_outbox o
		SET status = 'sending', lease_owner = $2,
			lease_until = NOW() + ($3 * INTERVAL '1 millisecond'), updated_at = NOW()
		FROM candidates c
		WHERE o.id = c.id
		RETURNING o.id, o.user_id, o.event_type, o.message, o.attempts`,
		limit, owner, lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]service.WeChatBotOutboxItem, 0, limit)
	for rows.Next() {
		var item service.WeChatBotOutboxItem
		if err := rows.Scan(&item.ID, &item.UserID, &item.EventType, &item.Message, &item.Attempts); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *weChatBotRepository) MarkOutboxSent(ctx context.Context, id int64, owner string, sentAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_outbox SET status = 'sent', sent_at = $1,
			lease_owner = '', lease_until = NULL, last_error = '', updated_at = NOW()
		WHERE id = $2 AND status = 'sending' AND lease_owner = $3`, sentAt, id, owner)
	return err
}

func (r *weChatBotRepository) RetryOutbox(ctx context.Context, id int64, owner, status, lastError string, availableAt time.Time) error {
	if status != "blocked" && status != "retry" {
		status = "retry"
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_outbox SET status = $1, attempts = attempts + 1, available_at = $2,
			lease_owner = '', lease_until = NULL, last_error = $3, updated_at = NOW()
		WHERE id = $4 AND status = 'sending' AND lease_owner = $5`,
		status, availableAt, boundedWeChatRepoError(lastError), id, owner)
	return err
}

func (r *weChatBotRepository) ReleasePendingForUser(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE wechat_bot_outbox SET available_at = NOW(), status = 'pending', updated_at = NOW()
		WHERE user_id = $1 AND status IN ('blocked', 'retry')`, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *weChatBotRepository) GetAdminMetrics(ctx context.Context) (int64, int64, int64, int64, error) {
	var connected, deliveryOpen, pending, withErrors int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE bot_token_encrypted <> ''),
			COUNT(*) FILTER (WHERE bot_token_encrypted <> '' AND enabled
				AND last_inbound_at >= NOW() - INTERVAL '24 hours' AND outbound_count < 10),
			COUNT(*) FILTER (WHERE last_error <> '')
		FROM wechat_bot_accounts`).Scan(&connected, &deliveryOpen, &withErrors); err != nil {
		return 0, 0, 0, 0, err
	}
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wechat_bot_outbox
		WHERE status IN ('pending', 'blocked', 'retry', 'sending')`).Scan(&pending); err != nil {
		return 0, 0, 0, 0, err
	}
	return connected, deliveryOpen, pending, withErrors, nil
}

func (r *weChatBotRepository) PruneOutbox(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM wechat_bot_outbox
		WHERE status IN ('sent', 'cancelled') AND updated_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

const weChatAccountSelect = `
	SELECT user_id, bot_token_encrypted, base_url, bot_id, ilink_user_id, get_updates_buf,
	       context_token_encrypted, enabled, notify_admin, notify_balance, notify_login,
	       chat_enabled, chat_api_key_id, chat_model, chat_history_encrypted,
	       last_inbound_at, last_outbound_at, outbound_count,
	       last_connected_at, last_error, created_at, updated_at
	FROM wechat_bot_accounts`

type weChatRowScanner interface {
	Scan(dest ...any) error
}

func scanWeChatAccount(row weChatRowScanner) (*service.WeChatBotAccount, error) {
	account := &service.WeChatBotAccount{}
	if err := row.Scan(
		&account.UserID, &account.BotTokenEncrypted, &account.BaseURL, &account.BotID,
		&account.ILinkUserID, &account.GetUpdatesBuf, &account.ContextTokenEncrypted,
		&account.Enabled, &account.NotifyAdmin, &account.NotifyBalance, &account.NotifyLogin,
		&account.ChatEnabled, &account.ChatAPIKeyID, &account.ChatModel, &account.ChatHistoryEncrypted,
		&account.LastInboundAt, &account.LastOutboundAt, &account.OutboundCount,
		&account.LastConnectedAt, &account.LastError, &account.CreatedAt, &account.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrWeChatBotAccountMissing
		}
		return nil, err
	}
	return account, nil
}

func boundedWeChatRepoError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

var _ service.WeChatBotRepository = (*weChatBotRepository)(nil)

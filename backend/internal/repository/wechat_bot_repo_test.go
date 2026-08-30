package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestWeChatBotSaveLoginRejectsIdentityOwnedByAnotherUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	repo := &weChatBotRepository{db: db}
	account := &service.WeChatBotAccount{
		BotTokenEncrypted: "encrypted-token",
		BaseURL:           "https://ilinkai.weixin.qq.com",
		BotID:             "bot-1",
		ILinkUserID:       "wx-user-1",
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtext($1))`)).
		WithArgs("bot:bot-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtext($1))`)).
		WithArgs("user:wx-user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT user_id FROM wechat_bot_accounts
		WHERE user_id <> $1
		  AND (($2 <> '' AND bot_id = $2) OR ($3 <> '' AND ilink_user_id = $3))
		LIMIT 1 FOR UPDATE`)).
		WithArgs(int64(11), "bot-1", "wx-user-1").
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(22)))
	mock.ExpectRollback()

	err = repo.SaveLogin(context.Background(), 11, account)
	if !errors.Is(err, service.ErrWeChatBotIdentityBound) {
		t.Fatalf("SaveLogin() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestWeChatBotUpdateSettingsCancelsDisabledNotificationCategories(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	repo := &weChatBotRepository{db: db}
	update := service.WeChatBotSettingsUpdate{
		Enabled:       true,
		NotifyAdmin:   false,
		NotifyBalance: true,
		NotifyLogin:   false,
		ChatModel:     "gpt-4o-mini",
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`
		UPDATE wechat_bot_accounts SET
			enabled = $1, notify_admin = $2, notify_balance = $3, notify_login = $4,
			chat_enabled = $5, chat_api_key_id = $6, chat_model = $7, updated_at = NOW()
		WHERE user_id = $8`)).
		WithArgs(true, false, true, false, false, nil, "gpt-4o-mini", int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`
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
		  )`)).
		WithArgs(int64(11), true, false, true, false).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	if err := repo.UpdateAccountSettings(context.Background(), 11, update); err != nil {
		t.Fatalf("UpdateAccountSettings() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

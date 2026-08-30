package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	WeChatBotEventAdmin   = "admin"
	WeChatBotEventBalance = "balance"
	WeChatBotEventLogin   = "login"
)

var (
	ErrWeChatBotAccountMissing = infraerrors.NotFound("WECHAT_BOT_ACCOUNT_NOT_FOUND", "WeChat bot account was not found")
	ErrWeChatBotIdentityBound  = infraerrors.Conflict("WECHAT_BOT_IDENTITY_BOUND", "This WeChat account is already bound to another user")
	ErrWeChatBotAPIKey         = infraerrors.BadRequest("WECHAT_BOT_API_KEY_INVALID", "The selected API key is not available")
)

type WeChatBotAccount struct {
	UserID                int64
	BotTokenEncrypted     string
	BaseURL               string
	BotID                 string
	ILinkUserID           string
	GetUpdatesBuf         string
	LastConnectedAt       *time.Time
	LastError             string
	ContextTokenEncrypted string
	Enabled               bool
	NotifyAdmin           bool
	NotifyBalance         bool
	NotifyLogin           bool
	ChatEnabled           bool
	ChatAPIKeyID          *int64
	ChatModel             string
	ChatHistoryEncrypted  string
	LastInboundAt         *time.Time
	LastOutboundAt        *time.Time
	OutboundCount         int
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type WeChatBotSettingsUpdate struct {
	Enabled       bool
	NotifyAdmin   bool
	NotifyBalance bool
	NotifyLogin   bool
	ChatEnabled   bool
	ChatAPIKeyID  *int64
	ChatModel     string
}

type WeChatBotOutboxItem struct {
	ID        int64
	UserID    int64
	EventType string
	Message   string
	Attempts  int
}

type WeChatBotRepository interface {
	GetAccount(ctx context.Context, userID int64) (*WeChatBotAccount, error)
	SaveLogin(ctx context.Context, userID int64, account *WeChatBotAccount) error
	ClearLogin(ctx context.Context, userID int64, lastError string) error
	DeleteAccount(ctx context.Context, userID int64) error
	ListLoggedInUserIDs(ctx context.Context) ([]int64, error)
	UpdateCursor(ctx context.Context, userID int64, cursor string, connectedAt time.Time) error
	SetAccountError(ctx context.Context, userID int64, lastError string) error
	AcquirePollerLease(ctx context.Context, userID int64, owner string, lease time.Duration) (bool, error)
	UpdateAccountSettings(ctx context.Context, userID int64, update WeChatBotSettingsUpdate) error
	TouchInbound(ctx context.Context, userID int64, ilinkUserID, encryptedContextToken string, inboundAt time.Time) (*WeChatBotAccount, error)
	IncrementOutbound(ctx context.Context, userID int64, sentAt time.Time) error
	SaveChatHistory(ctx context.Context, userID int64, encryptedHistory string) error
	ClearChatHistory(ctx context.Context, userID int64) error

	EnqueueNotification(ctx context.Context, userID int64, eventType, message string) error
	EnqueueAdminBroadcast(ctx context.Context, userIDs []int64, message string) (int64, error)
	ClaimOutbox(ctx context.Context, owner string, limit int, lease time.Duration) ([]WeChatBotOutboxItem, error)
	MarkOutboxSent(ctx context.Context, id int64, owner string, sentAt time.Time) error
	RetryOutbox(ctx context.Context, id int64, owner, status, lastError string, availableAt time.Time) error
	ReleasePendingForUser(ctx context.Context, userID int64) (int64, error)
	GetAdminMetrics(ctx context.Context) (connected int64, deliveryOpen int64, pending int64, withErrors int64, err error)
	PruneOutbox(ctx context.Context, before time.Time) (int64, error)
}

type WeChatBotUserStatus struct {
	Available       bool       `json:"available"`
	LoggedIn        bool       `json:"logged_in"`
	BotID           string     `json:"bot_id,omitempty"`
	ILinkUserID     string     `json:"ilink_user_id,omitempty"`
	Enabled         bool       `json:"enabled"`
	NotifyAdmin     bool       `json:"notify_admin"`
	NotifyBalance   bool       `json:"notify_balance"`
	NotifyLogin     bool       `json:"notify_login"`
	ChatEnabled     bool       `json:"chat_enabled"`
	ChatAPIKeyID    *int64     `json:"chat_api_key_id,omitempty"`
	ChatModel       string     `json:"chat_model"`
	LastInboundAt   *time.Time `json:"last_inbound_at,omitempty"`
	LastConnectedAt *time.Time `json:"last_connected_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	DeliveryOpen    bool       `json:"delivery_open"`
	OutboundCount   int        `json:"outbound_count"`
}

type WeChatBotAdminStatus struct {
	Running           bool  `json:"running"`
	ConnectedUsers    int64 `json:"connected_users"`
	DeliveryOpenUsers int64 `json:"delivery_open_users"`
	PendingMessages   int64 `json:"pending_messages"`
	UsersWithErrors   int64 `json:"users_with_errors"`
}

type WeChatBotLoginQRResult struct {
	LoginID            string    `json:"login_id"`
	QRCode             string    `json:"qrcode"`
	QRCodeImageContent string    `json:"qrcode_img_content,omitempty"`
	URL                string    `json:"url,omitempty"`
	ExpiresAt          time.Time `json:"expires_at"`
}

type WeChatBotLoginStatus struct {
	Status   string `json:"status"`
	LoggedIn bool   `json:"logged_in"`
	BotID    string `json:"bot_id,omitempty"`
}

type WeChatBotNotifier interface {
	NotifyUser(ctx context.Context, userID int64, eventType, message string)
}

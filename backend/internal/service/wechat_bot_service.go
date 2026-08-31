package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/google/uuid"
)

const (
	weChatBotLoginSessionTTL = 2 * time.Minute
	weChatBotPollerLease     = 55 * time.Second
	weChatBotDeliveryLease   = 45 * time.Second
	weChatBotDeliveryBatch   = 20
	weChatBotOutboxRetention = 30 * 24 * time.Hour
	weChatBotMaxMessageRunes = 4800
	weChatBotMaxHistory      = 12
	weChatBotNotifyQueueSize = 1024
	weChatBotChatConcurrency = 16
)

type weChatBotLoginSession struct {
	UserID    int64
	QRCode    string
	BaseURL   string
	ExpiresAt time.Time
}

type weChatBotNotification struct {
	UserID    int64
	EventType string
	Message   string
}

type weChatBotChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type WeChatBotService struct {
	repo       WeChatBotRepository
	userRepo   UserRepository
	apiKeyRepo APIKeyRepository
	encryptor  SecretEncryptor
	cfg        *config.Config
	ilink      *WeChatILinkClient
	chatHTTP   *http.Client
	workerID   string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	chatWG sync.WaitGroup
	start  sync.Once
	stop   sync.Once

	running   atomic.Bool
	sendLocks [64]sync.Mutex
	loginMu   sync.Mutex
	logins    map[string]*weChatBotLoginSession
	chatBusy  sync.Map
	chatSlots chan struct{}
	notify    chan weChatBotNotification
}

func NewWeChatBotService(
	repo WeChatBotRepository,
	userRepo UserRepository,
	apiKeyRepo APIKeyRepository,
	encryptor SecretEncryptor,
	cfg *config.Config,
) *WeChatBotService {
	ctx, cancel := context.WithCancel(context.Background())
	return &WeChatBotService{
		repo: repo, userRepo: userRepo, apiKeyRepo: apiKeyRepo, encryptor: encryptor, cfg: cfg,
		ilink: NewWeChatILinkClient(),
		chatHTTP: &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		workerID: uuid.NewString(), ctx: ctx, cancel: cancel,
		logins:    make(map[string]*weChatBotLoginSession),
		chatSlots: make(chan struct{}, weChatBotChatConcurrency),
		notify:    make(chan weChatBotNotification, weChatBotNotifyQueueSize),
	}
}

func ProvideWeChatBotService(
	repo WeChatBotRepository,
	userRepo UserRepository,
	apiKeyRepo APIKeyRepository,
	encryptor SecretEncryptor,
	cfg *config.Config,
) *WeChatBotService {
	svc := NewWeChatBotService(repo, userRepo, apiKeyRepo, encryptor, cfg)
	svc.Start()
	return svc
}

func (s *WeChatBotService) Start() {
	if s == nil || s.repo == nil || s.encryptor == nil {
		return
	}
	s.start.Do(func() {
		s.running.Store(true)
		s.wg.Add(3)
		go s.pollManagerLoop()
		go s.deliveryLoop()
		go s.notificationLoop()
	})
}

func (s *WeChatBotService) Stop() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		s.cancel()
		s.wg.Wait()
		s.chatWG.Wait()
		s.running.Store(false)
	})
}

func (s *WeChatBotService) NotifyUser(_ context.Context, userID int64, eventType, message string) {
	if s == nil || userID <= 0 || !validWeChatBotEvent(eventType) {
		return
	}
	message = boundedWeChatBotMessage(message)
	if message == "" {
		return
	}
	select {
	case s.notify <- weChatBotNotification{UserID: userID, EventType: eventType, Message: message}:
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.repo.EnqueueNotification(ctx, userID, eventType, message); err != nil {
			slog.Warn("failed to persist wechat notification after queue saturation", "user_id", userID, "event_type", eventType, "error", err)
		}
	}
}

func (s *WeChatBotService) GetUserStatus(ctx context.Context, userID int64) (*WeChatBotUserStatus, error) {
	result := &WeChatBotUserStatus{
		Available: s.running.Load(),
		Enabled:   true, NotifyAdmin: false, NotifyBalance: false, NotifyLogin: false,
		ChatModel: "gpt-4o-mini",
	}
	account, err := s.repo.GetAccount(ctx, userID)
	if errors.Is(err, ErrWeChatBotAccountMissing) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	result.LoggedIn = account.BotTokenEncrypted != ""
	result.BotID = account.BotID
	result.ILinkUserID = account.ILinkUserID
	result.Enabled = account.Enabled
	result.NotifyAdmin = account.NotifyAdmin
	result.NotifyBalance = account.NotifyBalance
	result.NotifyLogin = account.NotifyLogin
	result.ChatEnabled = account.ChatEnabled
	result.ChatAPIKeyID = account.ChatAPIKeyID
	result.ChatModel = account.ChatModel
	result.LastInboundAt = account.LastInboundAt
	result.LastConnectedAt = account.LastConnectedAt
	result.LastError = account.LastError
	result.OutboundCount = account.OutboundCount
	result.DeliveryOpen = weChatBotDeliveryOpen(account, time.Now())
	return result, nil
}

func (s *WeChatBotService) GetAdminStatus(ctx context.Context) (*WeChatBotAdminStatus, error) {
	connected, deliveryOpen, pending, withErrors, err := s.repo.GetAdminMetrics(ctx)
	if err != nil {
		return nil, err
	}
	return &WeChatBotAdminStatus{
		Running: s.running.Load(), ConnectedUsers: connected, DeliveryOpenUsers: deliveryOpen,
		PendingMessages: pending, UsersWithErrors: withErrors,
	}, nil
}

func (s *WeChatBotService) UpdateUserSettings(ctx context.Context, userID int64, update WeChatBotSettingsUpdate) (*WeChatBotUserStatus, error) {
	update.ChatModel = normalizeWeChatBotModel(update.ChatModel)
	if update.ChatModel == "" {
		return nil, ErrWeChatBotAPIKey
	}
	if update.ChatAPIKeyID != nil {
		if _, err := s.resolveChatAPIKey(ctx, userID, *update.ChatAPIKeyID); err != nil {
			return nil, err
		}
	} else if update.ChatEnabled {
		return nil, ErrWeChatBotAPIKey
	}
	if err := s.repo.UpdateAccountSettings(ctx, userID, update); err != nil {
		return nil, err
	}
	return s.GetUserStatus(ctx, userID)
}

func (s *WeChatBotService) Disconnect(ctx context.Context, userID int64) error {
	s.loginMu.Lock()
	for loginID, session := range s.logins {
		if session.UserID == userID {
			delete(s.logins, loginID)
		}
	}
	s.loginMu.Unlock()
	return s.repo.DeleteAccount(ctx, userID)
}

func (s *WeChatBotService) SendTestNotification(ctx context.Context, userID int64) error {
	status, err := s.GetUserStatus(ctx, userID)
	if err != nil {
		return err
	}
	if !status.LoggedIn {
		return ErrWeChatBotAccountMissing
	}
	return s.repo.EnqueueNotification(ctx, userID, WeChatBotEventAdmin, "[Sub2API] 微信通知测试成功。")
}

func (s *WeChatBotService) BroadcastAdmin(ctx context.Context, title, message string, userIDs []int64) (int64, error) {
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	if message == "" {
		return 0, errors.New("broadcast message is required")
	}
	if title != "" {
		message = title + "\n\n" + message
	}
	return s.repo.EnqueueAdminBroadcast(ctx, userIDs, boundedWeChatBotMessage("[管理员通知]\n"+message))
}

func (s *WeChatBotService) CreateLoginQR(ctx context.Context, userID int64) (*WeChatBotLoginQRResult, error) {
	qr, err := s.ilink.GetQRCode(ctx)
	if err != nil {
		return nil, err
	}
	loginID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(weChatBotLoginSessionTTL)
	s.loginMu.Lock()
	for existingID, session := range s.logins {
		if session.UserID == userID || time.Now().After(session.ExpiresAt) {
			delete(s.logins, existingID)
		}
	}
	s.logins[loginID] = &weChatBotLoginSession{
		UserID: userID, QRCode: qr.QRCode, BaseURL: weChatILinkBaseURL, ExpiresAt: expiresAt,
	}
	s.loginMu.Unlock()
	return &WeChatBotLoginQRResult{
		LoginID: loginID, QRCode: qr.QRCode, QRCodeImageContent: qr.QRCodeImageContent,
		URL: qr.URL, ExpiresAt: expiresAt,
	}, nil
}

func (s *WeChatBotService) PollLoginQR(ctx context.Context, userID int64, loginID string) (*WeChatBotLoginStatus, error) {
	s.loginMu.Lock()
	session := s.logins[loginID]
	if session == nil || session.UserID != userID {
		s.loginMu.Unlock()
		return &WeChatBotLoginStatus{Status: "expired"}, nil
	}
	if time.Now().After(session.ExpiresAt) {
		delete(s.logins, loginID)
		s.loginMu.Unlock()
		return &WeChatBotLoginStatus{Status: "expired"}, nil
	}
	baseURL, qrcode := session.BaseURL, session.QRCode
	s.loginMu.Unlock()

	status, err := s.ilink.PollQRCode(ctx, baseURL, qrcode)
	if err != nil {
		return nil, err
	}
	if status.Status == "scaned_but_redirect" && status.RedirectHost != "" {
		redirectURL, normalizeErr := normalizeWeChatILinkURL("https://" + status.RedirectHost)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		s.loginMu.Lock()
		if current := s.logins[loginID]; current != nil && current.UserID == userID && current.QRCode == qrcode {
			current.BaseURL = redirectURL
		}
		s.loginMu.Unlock()
	}
	result := &WeChatBotLoginStatus{Status: status.Status}
	if status.Status != "confirmed" {
		if status.Status == "expired" {
			s.loginMu.Lock()
			delete(s.logins, loginID)
			s.loginMu.Unlock()
		}
		return result, nil
	}
	if status.BotToken == "" || status.BotID == "" {
		return nil, errors.New("wechat ilink login response is incomplete")
	}
	confirmedBaseURL := status.BaseURL
	if strings.TrimSpace(confirmedBaseURL) == "" {
		confirmedBaseURL = baseURL
	}
	stateBaseURL, err := normalizeWeChatILinkURL(confirmedBaseURL)
	if err != nil {
		return nil, err
	}
	encryptedToken, err := s.encryptor.Encrypt(status.BotToken)
	if err != nil {
		return nil, fmt.Errorf("encrypt wechat ilink token: %w", err)
	}
	account := &WeChatBotAccount{
		UserID:            userID,
		BotTokenEncrypted: encryptedToken,
		BaseURL:           stateBaseURL,
		BotID:             status.BotID,
		ILinkUserID:       status.UserID,
	}

	// Serialize the final write with Disconnect. A revoked or superseded QR
	// session must not restore an account after the user has disconnected.
	s.loginMu.Lock()
	current := s.logins[loginID]
	if current == nil || current.UserID != userID || current.QRCode != qrcode || time.Now().After(current.ExpiresAt) {
		if current != nil && time.Now().After(current.ExpiresAt) {
			delete(s.logins, loginID)
		}
		s.loginMu.Unlock()
		return &WeChatBotLoginStatus{Status: "expired"}, nil
	}
	err = s.repo.SaveLogin(ctx, userID, account)
	if err == nil {
		delete(s.logins, loginID)
	}
	s.loginMu.Unlock()
	if err != nil {
		return nil, err
	}
	result.LoggedIn = true
	result.BotID = status.BotID
	return result, nil
}

func (s *WeChatBotService) notificationLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case event := <-s.notify:
			ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
			err := s.repo.EnqueueNotification(ctx, event.UserID, event.EventType, event.Message)
			cancel()
			if err != nil && s.ctx.Err() == nil {
				slog.Warn("failed to persist wechat bot notification", "user_id", event.UserID, "event_type", event.EventType, "error", err)
			}
		}
	}
}

func (s *WeChatBotService) pollManagerLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	pollers := make(map[int64]context.CancelFunc)
	done := make(chan int64, 256)
	var workers sync.WaitGroup

	reconcile := func() {
		ids, err := s.repo.ListLoggedInUserIDs(s.ctx)
		if err != nil {
			if s.ctx.Err() == nil {
				slog.Warn("failed to list connected wechat bot accounts", "error", err)
			}
			return
		}
		desired := make(map[int64]struct{}, len(ids))
		for _, userID := range ids {
			desired[userID] = struct{}{}
			if _, exists := pollers[userID]; exists {
				continue
			}
			pollCtx, cancel := context.WithCancel(s.ctx)
			pollers[userID] = cancel
			workers.Add(1)
			go func() {
				defer workers.Done()
				s.pollAccountLoop(pollCtx, userID)
				select {
				case done <- userID:
				case <-s.ctx.Done():
				}
			}()
		}
		for userID, cancel := range pollers {
			if _, exists := desired[userID]; !exists {
				cancel()
				delete(pollers, userID)
			}
		}
	}

	reconcile()
	for {
		select {
		case <-s.ctx.Done():
			for _, cancel := range pollers {
				cancel()
			}
			workers.Wait()
			return
		case userID := <-done:
			if cancel, exists := pollers[userID]; exists {
				cancel()
				delete(pollers, userID)
			}
		case <-ticker.C:
			reconcile()
		}
	}
}

func (s *WeChatBotService) pollAccountLoop(ctx context.Context, userID int64) {
	consecutiveErrors := 0
	for ctx.Err() == nil {
		account, err := s.repo.GetAccount(ctx, userID)
		if errors.Is(err, ErrWeChatBotAccountMissing) || (err == nil && account.BotTokenEncrypted == "") {
			return
		}
		if err != nil {
			consecutiveErrors++
			if !waitWeChatBotPollBackoff(ctx, consecutiveErrors) {
				return
			}
			continue
		}
		lease, err := s.repo.AcquirePollerLease(ctx, userID, s.workerID, weChatBotPollerLease)
		if err != nil || !lease {
			if !waitWeChatBotContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		botToken, err := s.encryptor.Decrypt(account.BotTokenEncrypted)
		if err != nil {
			_ = s.repo.SetAccountError(ctx, userID, "cannot decrypt bot login token")
			if !waitWeChatBotPollBackoff(ctx, 1) {
				return
			}
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, 48*time.Second)
		messages, cursor, err := s.ilink.GetUpdates(pollCtx, account.BaseURL, botToken, account.GetUpdatesBuf)
		cancel()
		if err != nil {
			if errors.Is(err, ErrWeChatILinkAuthExpired) {
				_ = s.repo.ClearLogin(ctx, userID, "iLink login expired; scan the QR code again")
				return
			}
			if ctx.Err() != nil {
				return
			}
			consecutiveErrors++
			_ = s.repo.SetAccountError(ctx, userID, boundedWeChatBotInternalError(err))
			if !waitWeChatBotPollBackoff(ctx, consecutiveErrors) {
				return
			}
			continue
		}
		consecutiveErrors = 0
		if cursor == "" {
			cursor = account.GetUpdatesBuf
		}
		_ = s.repo.UpdateCursor(ctx, userID, cursor, time.Now().UTC())
		for i := range messages {
			s.handleInboundMessage(userID, messages[i])
		}
	}
}

func (s *WeChatBotService) deliveryLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	pruneTicker := time.NewTicker(24 * time.Hour)
	defer pruneTicker.Stop()
	s.pruneOutbox()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-pruneTicker.C:
			s.pruneOutbox()
		case <-ticker.C:
			items, err := s.repo.ClaimOutbox(s.ctx, s.workerID, weChatBotDeliveryBatch, weChatBotDeliveryLease)
			if err != nil {
				slog.Warn("failed to claim wechat bot notifications", "error", err)
				continue
			}
			for i := range items {
				s.deliverOutbox(items[i])
			}
		}
	}
}

func (s *WeChatBotService) pruneOutbox() {
	if _, err := s.repo.PruneOutbox(s.ctx, time.Now().UTC().Add(-weChatBotOutboxRetention)); err != nil && s.ctx.Err() == nil {
		slog.Warn("failed to prune wechat bot outbox", "error", err)
	}
}

func (s *WeChatBotService) deliverOutbox(item WeChatBotOutboxItem) {
	account, err := s.repo.GetAccount(s.ctx, item.UserID)
	if err == nil && !weChatBotEventEnabled(account, item.EventType) {
		// UpdateAccountSettings atomically cancels the claimed row. Avoid sending
		// a stale in-memory claim after the user has opted out.
		return
	}
	if err == nil && !weChatBotDeliveryOpen(account, time.Now()) {
		err = ErrWeChatILinkWindow
	}
	if err == nil {
		err = s.sendToAccountWithClientID(s.ctx, account, item.Message, fmt.Sprintf("sub2api:outbox:%d", item.ID))
	}
	if err == nil {
		_ = s.repo.MarkOutboxSent(s.ctx, item.ID, s.workerID, time.Now().UTC())
		return
	}
	status := "retry"
	delay := weChatBotRetryDelay(item.Attempts + 1)
	if errors.Is(err, ErrWeChatILinkWindow) {
		status = "blocked"
		delay = 10 * time.Minute
	}
	if errors.Is(err, ErrWeChatILinkAuthExpired) {
		_ = s.repo.ClearLogin(s.ctx, item.UserID, "iLink login expired; scan the QR code again")
		delay = 5 * time.Minute
	}
	_ = s.repo.RetryOutbox(s.ctx, item.ID, s.workerID, status, boundedWeChatBotInternalError(err), time.Now().UTC().Add(delay))
}

func (s *WeChatBotService) handleInboundMessage(userID int64, message WeChatILinkMessage) {
	if message.MessageType != 1 || strings.TrimSpace(message.FromUserID) == "" {
		return
	}
	text := message.Text()
	if text == "" {
		return
	}
	encryptedContext := ""
	if message.ContextToken != "" {
		var err error
		encryptedContext, err = s.encryptor.Encrypt(message.ContextToken)
		if err != nil {
			slog.Warn("failed to encrypt wechat context token", "error", err)
			return
		}
	}
	now := time.Now().UTC()
	account, err := s.repo.TouchInbound(s.ctx, userID, message.FromUserID, encryptedContext, now)
	if err != nil {
		slog.Warn("rejected wechat inbound message for user account", "user_id", userID, "error", err)
		return
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		reply := s.handleCommand(s.ctx, account, text)
		if reply != "" {
			s.replyToInbound(account, reply)
		}
		return
	}
	if !account.Enabled {
		s.replyToInbound(account, "微信 Bot 当前已在 Sub2API 个人资料中停用，请先开启并保存后再试。")
		return
	}
	if !account.ChatEnabled {
		s.replyToInbound(account, "API Key 聊天尚未开启。请在 Sub2API 个人资料中选择 API Key、开启聊天并保存；也可发送 /help 查看命令。")
		return
	}
	if _, busy := s.chatBusy.LoadOrStore(account.UserID, struct{}{}); busy {
		s.replyToInbound(account, "上一条消息仍在处理中，请稍后再试。")
		return
	}
	select {
	case s.chatSlots <- struct{}{}:
	case <-s.ctx.Done():
		s.chatBusy.Delete(account.UserID)
		return
	default:
		s.chatBusy.Delete(account.UserID)
		s.replyToInbound(account, "聊天服务当前繁忙，请稍后再试。")
		return
	}
	s.chatWG.Add(1)
	go func(account *WeChatBotAccount, prompt string) {
		defer s.chatWG.Done()
		defer s.chatBusy.Delete(account.UserID)
		defer func() { <-s.chatSlots }()
		reply, chatErr := s.chat(s.ctx, account, prompt)
		if chatErr != nil {
			reply = "AI 对话失败：" + boundedWeChatBotUserError(chatErr)
		}
		if reply != "" {
			s.replyToInbound(account, reply)
		}
	}(account, text)
}

func (s *WeChatBotService) replyToInbound(account *WeChatBotAccount, text string) {
	if err := s.sendToAccount(s.ctx, account, text); err != nil {
		userID := int64(0)
		if account != nil {
			userID = account.UserID
		}
		slog.Warn("failed to reply to wechat inbound message", "user_id", userID, "error", err)
	}
}

func (s *WeChatBotService) handleCommand(ctx context.Context, account *WeChatBotAccount, input string) string {
	parts := strings.Fields(strings.TrimSpace(input))
	if len(parts) == 0 {
		return ""
	}
	command := strings.ToLower(parts[0])
	switch command {
	case "/help", "/帮助":
		return "可用命令\n/balance 查询余额\n/status 查看通知和聊天状态\n/keys 查看可用 API Key\n/key <ID> 选择聊天 Key\n/model <模型> 设置聊天模型\n/chat on|off 开关聊天\n/notify on|off 总开关\n/admin on|off 管理员通知\n/balance-notify on|off 余额通知\n/login-notify on|off 登录通知\n/clear 清除聊天记录\n/pull 恢复待发送通知"
	case "/balance", "/余额":
		user, err := s.userRepo.GetByID(ctx, account.UserID)
		if err != nil {
			return "余额查询失败，请稍后重试。"
		}
		return fmt.Sprintf("账户余额：$%.4f\n冻结余额：$%.4f", user.Balance, user.FrozenBalance)
	case "/status", "/状态":
		return formatWeChatBotAccountStatus(account)
	case "/keys":
		keys, _, err := s.apiKeyRepo.ListByUserID(ctx, account.UserID, pagination.PaginationParams{Page: 1, PageSize: 20}, APIKeyListFilters{})
		if err != nil {
			return "API Key 查询失败，请稍后重试。"
		}
		lines := []string{"可用 API Key："}
		for i := range keys {
			if keys[i].IsActive() && !keys[i].IsExpired() && !keys[i].IsQuotaExhausted() {
				selected := ""
				if account.ChatAPIKeyID != nil && *account.ChatAPIKeyID == keys[i].ID {
					selected = "（当前）"
				}
				lines = append(lines, fmt.Sprintf("%d - %s%s", keys[i].ID, keys[i].Name, selected))
			}
		}
		if len(lines) == 1 {
			return "没有可用于聊天的 API Key。"
		}
		return strings.Join(lines, "\n") + "\n使用 /key <ID> 选择。"
	case "/key":
		if len(parts) != 2 {
			return "用法：/key <API Key ID>"
		}
		keyID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || keyID <= 0 {
			return "API Key ID 无效。"
		}
		if _, err := s.resolveChatAPIKey(ctx, account.UserID, keyID); err != nil {
			return "该 API Key 不存在、不可用或不属于当前账号。"
		}
		account.ChatAPIKeyID = &keyID
		account.ChatEnabled = true
		if err := s.repo.UpdateAccountSettings(ctx, account.UserID, accountSettings(account)); err != nil {
			return "保存 API Key 失败，请稍后重试。"
		}
		return fmt.Sprintf("已选择 API Key %d，并开启聊天。", keyID)
	case "/model":
		if len(parts) != 2 {
			return "用法：/model <模型名称>"
		}
		model := normalizeWeChatBotModel(parts[1])
		if model == "" {
			return "模型名称无效。"
		}
		account.ChatModel = model
		if err := s.repo.UpdateAccountSettings(ctx, account.UserID, accountSettings(account)); err != nil {
			return "保存模型失败，请稍后重试。"
		}
		return "聊天模型已设置为：" + model
	case "/chat":
		value, ok := parseWeChatBotToggle(parts)
		if !ok {
			return "用法：/chat on 或 /chat off"
		}
		if value && account.ChatAPIKeyID == nil {
			return "请先使用 /keys 查看并通过 /key <ID> 选择 API Key。"
		}
		account.ChatEnabled = value
	case "/notify":
		value, ok := parseWeChatBotToggle(parts)
		if !ok {
			return "用法：/notify on 或 /notify off"
		}
		account.Enabled = value
	case "/admin":
		value, ok := parseWeChatBotToggle(parts)
		if !ok {
			return "用法：/admin on 或 /admin off"
		}
		account.NotifyAdmin = value
	case "/balance-notify":
		value, ok := parseWeChatBotToggle(parts)
		if !ok {
			return "用法：/balance-notify on 或 /balance-notify off"
		}
		account.NotifyBalance = value
	case "/login-notify":
		value, ok := parseWeChatBotToggle(parts)
		if !ok {
			return "用法：/login-notify on 或 /login-notify off"
		}
		account.NotifyLogin = value
	case "/clear", "/清除":
		if err := s.repo.ClearChatHistory(ctx, account.UserID); err != nil {
			return "清除聊天记录失败，请稍后重试。"
		}
		return "聊天记录已清除。"
	case "/pull":
		count, err := s.repo.ReleasePendingForUser(ctx, account.UserID)
		if err != nil {
			return "恢复待发送通知失败，请稍后重试。"
		}
		return fmt.Sprintf("已恢复 %d 条待发送通知。", count)
	case "/unbind":
		if len(parts) != 2 || !strings.EqualFold(parts[1], "confirm") {
			return "解绑会关闭微信通知。确认请发送：/unbind confirm"
		}
		if err := s.repo.DeleteAccount(ctx, account.UserID); err != nil {
			return "解绑失败，请稍后重试。"
		}
		return "微信通知已解绑。"
	default:
		return "未知命令。发送 /help 查看可用命令。"
	}
	if err := s.repo.UpdateAccountSettings(ctx, account.UserID, accountSettings(account)); err != nil {
		return "设置保存失败，请稍后重试。"
	}
	return "设置已更新。\n" + formatWeChatBotAccountStatus(account)
}

func (s *WeChatBotService) chat(ctx context.Context, account *WeChatBotAccount, prompt string) (string, error) {
	if account.ChatAPIKeyID == nil {
		return "", ErrWeChatBotAPIKey
	}
	apiKey, err := s.resolveChatAPIKey(ctx, account.UserID, *account.ChatAPIKeyID)
	if err != nil {
		return "", err
	}
	history, err := s.readChatHistory(account)
	if err != nil {
		slog.Warn("failed to read wechat chat history", "user_id", account.UserID, "error", err)
		history = nil
	}
	prompt = truncateRunes(strings.TrimSpace(prompt), 8000)
	history = append(history, weChatBotChatMessage{Role: "user", Content: prompt})
	if len(history) > weChatBotMaxHistory {
		history = history[len(history)-weChatBotMaxHistory:]
	}
	reply, err := s.callLocalChatGateway(ctx, apiKey.Key, account.ChatModel, history)
	if err != nil {
		return "", err
	}
	reply = truncateRunes(strings.TrimSpace(reply), weChatBotMaxMessageRunes)
	history = append(history, weChatBotChatMessage{Role: "assistant", Content: reply})
	if len(history) > weChatBotMaxHistory {
		history = history[len(history)-weChatBotMaxHistory:]
	}
	encoded, err := json.Marshal(history)
	if err == nil {
		var encrypted string
		encrypted, err = s.encryptor.Encrypt(string(encoded))
		if err == nil {
			err = s.repo.SaveChatHistory(ctx, account.UserID, encrypted)
			account.ChatHistoryEncrypted = encrypted
		}
	}
	if err != nil {
		slog.Warn("failed to persist wechat chat history", "user_id", account.UserID, "error", err)
	}
	return reply, nil
}

func (s *WeChatBotService) callLocalChatGateway(ctx context.Context, apiKey, model string, history []weChatBotChatMessage) (string, error) {
	port := 8080
	if s.cfg != nil && s.cfg.Server.Port > 0 {
		port = s.cfg.Server.Port
	}
	payload, err := json.Marshal(map[string]any{
		"model": model, "messages": history, "stream": false,
	})
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := s.chatHTTP.Do(req)
	if err != nil {
		return "", errors.New("聊天服务暂时不可用")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", errors.New("读取聊天响应失败")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiError struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &apiError)
		if apiError.Error.Message != "" {
			return "", errors.New(truncateRunes(apiError.Error.Message, 160))
		}
		return "", fmt.Errorf("聊天接口返回 HTTP %d", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		return "", errors.New("聊天接口未返回文本内容")
	}
	return result.Choices[0].Message.Content, nil
}

func (s *WeChatBotService) readChatHistory(account *WeChatBotAccount) ([]weChatBotChatMessage, error) {
	if account == nil || account.ChatHistoryEncrypted == "" {
		return nil, nil
	}
	plaintext, err := s.encryptor.Decrypt(account.ChatHistoryEncrypted)
	if err != nil {
		return nil, err
	}
	var history []weChatBotChatMessage
	if err := json.Unmarshal([]byte(plaintext), &history); err != nil {
		return nil, err
	}
	return history, nil
}

func (s *WeChatBotService) resolveChatAPIKey(ctx context.Context, userID, keyID int64) (*APIKey, error) {
	key, err := s.apiKeyRepo.GetByID(ctx, keyID)
	if err != nil || key == nil || key.UserID != userID || !key.IsActive() || key.IsExpired() || key.IsQuotaExhausted() {
		return nil, ErrWeChatBotAPIKey
	}
	return key, nil
}

func (s *WeChatBotService) sendToAccount(ctx context.Context, account *WeChatBotAccount, text string) error {
	return s.sendToAccountWithClientID(ctx, account, text, "")
}

func (s *WeChatBotService) sendToAccountWithClientID(ctx context.Context, account *WeChatBotAccount, text, clientID string) error {
	if account == nil || account.ILinkUserID == "" || account.BotTokenEncrypted == "" {
		return ErrWeChatBotAccountMissing
	}
	if !weChatBotDeliveryOpen(account, time.Now()) {
		return ErrWeChatILinkWindow
	}
	contextToken := ""
	if account.ContextTokenEncrypted != "" {
		var err error
		contextToken, err = s.encryptor.Decrypt(account.ContextTokenEncrypted)
		if err != nil {
			return fmt.Errorf("decrypt wechat context token: %w", err)
		}
	}
	token, err := s.encryptor.Decrypt(account.BotTokenEncrypted)
	if err != nil {
		return fmt.Errorf("decrypt wechat bot token: %w", err)
	}
	lock := &s.sendLocks[uint64(account.UserID)%uint64(len(s.sendLocks))]
	lock.Lock()
	err = s.ilink.SendTextWithClientID(ctx, account.BaseURL, token, account.ILinkUserID, boundedWeChatBotMessage(text), contextToken, clientID)
	lock.Unlock()
	if err != nil {
		if errors.Is(err, ErrWeChatILinkAuthExpired) {
			_ = s.repo.ClearLogin(ctx, account.UserID, "iLink login expired; scan the QR code again")
		}
		return err
	}
	if err := s.repo.IncrementOutbound(ctx, account.UserID, time.Now().UTC()); err != nil {
		// The remote send has already succeeded. Retrying would duplicate the message.
		slog.Warn("failed to record wechat outbound delivery", "user_id", account.UserID, "error", err)
		return nil
	}
	account.OutboundCount++
	return nil
}

func waitWeChatBotPollBackoff(ctx context.Context, failures int) bool {
	if failures < 1 {
		failures = 1
	}
	delay := time.Duration(failures*3) * time.Second
	if delay > time.Minute {
		delay = time.Minute
	}
	return waitWeChatBotContext(ctx, delay)
}

func accountSettings(account *WeChatBotAccount) WeChatBotSettingsUpdate {
	return WeChatBotSettingsUpdate{
		Enabled: account.Enabled, NotifyAdmin: account.NotifyAdmin,
		NotifyBalance: account.NotifyBalance, NotifyLogin: account.NotifyLogin,
		ChatEnabled: account.ChatEnabled, ChatAPIKeyID: account.ChatAPIKeyID, ChatModel: account.ChatModel,
	}
}

func formatWeChatBotAccountStatus(account *WeChatBotAccount) string {
	if account == nil {
		return "微信机器人未连接。"
	}
	return fmt.Sprintf(
		"微信 Bot 状态\n通知总开关：%s\n管理员通知：%s\n余额通知：%s\n登录通知：%s\n聊天：%s\n模型：%s\n连续下发：%d/10",
		weChatToggleLabel(account.Enabled), weChatToggleLabel(account.NotifyAdmin),
		weChatToggleLabel(account.NotifyBalance), weChatToggleLabel(account.NotifyLogin),
		weChatToggleLabel(account.ChatEnabled), account.ChatModel, account.OutboundCount,
	)
}

func weChatToggleLabel(value bool) string {
	if value {
		return "开启"
	}
	return "关闭"
}

func parseWeChatBotToggle(parts []string) (bool, bool) {
	if len(parts) != 2 {
		return false, false
	}
	switch strings.ToLower(parts[1]) {
	case "on", "1", "true", "开启":
		return true, true
	case "off", "0", "false", "关闭":
		return false, true
	default:
		return false, false
	}
}

func normalizeWeChatILinkURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return weChatILinkBaseURL, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return "", errors.New("wechat ilink returned an invalid service URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "weixin.qq.com" && !strings.HasSuffix(host, ".weixin.qq.com") {
		return "", errors.New("wechat ilink returned an untrusted service URL")
	}
	return "https://" + host, nil
}

func normalizeWeChatBotModel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 160 || !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ""
		}
	}
	return value
}

func boundedWeChatBotMessage(value string) string {
	return truncateRunes(strings.TrimSpace(value), weChatBotMaxMessageRunes)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func boundedWeChatBotInternalError(err error) string {
	if err == nil {
		return ""
	}
	return truncateRunes(strings.TrimSpace(err.Error()), 400)
}

func boundedWeChatBotUserError(err error) string {
	if err == nil {
		return "未知错误"
	}
	return truncateRunes(strings.TrimSpace(err.Error()), 160)
}

func validWeChatBotEvent(value string) bool {
	return value == WeChatBotEventAdmin || value == WeChatBotEventBalance || value == WeChatBotEventLogin
}

func weChatBotDeliveryOpen(account *WeChatBotAccount, now time.Time) bool {
	return account != nil && account.BotTokenEncrypted != "" && account.ILinkUserID != "" && account.LastInboundAt != nil &&
		now.Sub(*account.LastInboundAt) < 24*time.Hour && account.OutboundCount < 10
}

func weChatBotEventEnabled(account *WeChatBotAccount, eventType string) bool {
	if account == nil || account.BotTokenEncrypted == "" || !account.Enabled {
		return false
	}
	switch eventType {
	case WeChatBotEventAdmin:
		return account.NotifyAdmin
	case WeChatBotEventBalance:
		return account.NotifyBalance
	case WeChatBotEventLogin:
		return account.NotifyLogin
	default:
		return false
	}
}

func weChatBotRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(1<<min(attempt, 6)) * time.Second
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}

func waitWeChatBotContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var _ WeChatBotNotifier = (*WeChatBotService)(nil)

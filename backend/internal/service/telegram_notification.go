package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
)

const (
	telegramNotificationIngressQueueSize = 1024
	telegramNotificationPollInterval     = 500 * time.Millisecond
	telegramNotificationCleanupInterval  = time.Hour
	telegramNotificationClaimLease       = 30 * time.Second
	telegramNotificationBatchSize        = 25
	telegramNotificationDBTimeout        = 2 * time.Second
	telegramNotificationRetention        = 7 * 24 * time.Hour
	telegramNotificationMaxMessageBytes  = 4096
	telegramNotificationBatchingWindow   = 5 * time.Second
	telegramNotificationRateLimitPerHour = 60
	telegramNotificationRequestTimeout   = 10 * time.Second
)

// GatewayNotificationEventType describes the operator-visible gateway event.
// It deliberately has a small, platform-neutral vocabulary so future gateways
// can publish through the same transport without adding Telegram coupling.
type GatewayNotificationEventType string

const (
	GatewayNotificationEventError   GatewayNotificationEventType = "error"
	GatewayNotificationEventTimeout GatewayNotificationEventType = "timeout"
	GatewayNotificationEventSwitch  GatewayNotificationEventType = "switch"
)

// GatewayNotificationEvent contains only safe, diagnostic metadata. Request
// bodies, upstream response bodies, credentials, and user content are excluded
// before an event ever enters the persistent delivery outbox.
type GatewayNotificationEvent struct {
	Type            GatewayNotificationEventType `json:"type"`
	Platform        string                       `json:"platform,omitempty"`
	UserID          int64                        `json:"user_id,omitempty"`
	UserName        string                       `json:"user_name,omitempty"`
	AccountID       int64                        `json:"account_id,omitempty"`
	AccountName     string                       `json:"account_name,omitempty"`
	FromAccountID   int64                        `json:"from_account_id,omitempty"`
	FromAccountName string                       `json:"from_account_name,omitempty"`
	ToAccountID     int64                        `json:"to_account_id,omitempty"`
	ToAccountName   string                       `json:"to_account_name,omitempty"`
	Model           string                       `json:"model,omitempty"`
	StatusCode      int                          `json:"status_code,omitempty"`
	Stage           string                       `json:"stage,omitempty"`
	Reason          string                       `json:"reason,omitempty"`
	ElapsedMs       int64                        `json:"elapsed_ms,omitempty"`
	FromPriority    int                          `json:"from_priority,omitempty"`
	ToPriority      int                          `json:"to_priority,omitempty"`
	OccurredAt      time.Time                    `json:"occurred_at"`
}

// GatewayNotificationPublisher keeps gateway code independent from the
// delivery mechanism. PublishGatewayNotification must remain non-blocking.
type GatewayNotificationPublisher interface {
	PublishGatewayNotification(event GatewayNotificationEvent)
}

type gatewayNotificationPublisherHolder struct {
	publisher GatewayNotificationPublisher
}

type TelegramNotificationOutboxEvent struct {
	ID              int64
	Event           GatewayNotificationEvent
	OccurrenceCount int
	CreatedAt       time.Time
	LastOccurredAt  time.Time
	Attempts        int
}

// TelegramNotificationOutboxRepository provides durable cross-instance event
// deduplication and leased delivery claims.
type TelegramNotificationOutboxRepository interface {
	Enqueue(ctx context.Context, event GatewayNotificationEvent, dedupeKey string, dedupeBucket int64, availableAt time.Time) error
	Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]TelegramNotificationOutboxEvent, error)
	MarkDelivered(ctx context.Context, id int64, workerID string) error
	RetryClaimed(ctx context.Context, id int64, workerID string, availableAt time.Time, lastError string) error
	DeleteDeliveredBefore(ctx context.Context, before time.Time, limit int) (int64, error)
}

type telegramNotificationConfigSnapshot struct {
	cfg     config.TelegramNotificationConfig
	chatIDs []string
}

type telegramBotHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// TelegramNotificationService asynchronously persists and delivers gateway
// notifications. The request path only performs a bounded channel send.
type TelegramNotificationService struct {
	outbox             TelegramNotificationOutboxRepository
	httpClient         telegramBotHTTPClient
	telegramAPIBaseURL string
	workerID           string
	events             chan GatewayNotificationEvent
	config             atomic.Pointer[telegramNotificationConfigSnapshot]
	rateLimiter        *slidingWindowLimiter
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	start              sync.Once
	stop               sync.Once
	running            atomic.Bool
	ingressQueued      atomic.Uint64
	ingressDropped     atomic.Uint64
	persisted          atomic.Uint64
	delivered          atomic.Uint64
	suppressed         atomic.Uint64
	failures           atomic.Uint64
	lastError          atomic.Value
}

func NewTelegramNotificationService(
	outbox TelegramNotificationOutboxRepository,
	cfg *config.Config,
) *TelegramNotificationService {
	ctx, cancel := context.WithCancel(context.Background())
	snapshot := telegramNotificationConfigSnapshotFromConfig(cfg)
	queueSize := snapshot.cfg.QueueSize
	if queueSize <= 0 {
		queueSize = telegramNotificationIngressQueueSize
	}
	requestTimeout := time.Duration(snapshot.cfg.RequestTimeoutSeconds) * time.Second
	if requestTimeout <= 0 {
		requestTimeout = telegramNotificationRequestTimeout
	}
	svc := &TelegramNotificationService{
		outbox:             outbox,
		httpClient:         newTelegramNotificationHTTPClient(requestTimeout),
		telegramAPIBaseURL: "https://api.telegram.org",
		workerID:           uuid.NewString(),
		events:             make(chan GatewayNotificationEvent, queueSize),
		rateLimiter:        newSlidingWindowLimiter(telegramNotificationRateLimitPerHour, time.Hour),
		ctx:                ctx,
		cancel:             cancel,
	}
	svc.config.Store(snapshot)
	svc.lastError.Store("")
	return svc
}

func ProvideTelegramNotificationService(
	outbox TelegramNotificationOutboxRepository,
	cfg *config.Config,
) *TelegramNotificationService {
	svc := NewTelegramNotificationService(outbox, cfg)
	svc.Start()
	return svc
}

func (s *TelegramNotificationService) Start() {
	if s == nil || s.outbox == nil {
		return
	}
	s.start.Do(func() {
		s.running.Store(true)
		s.wg.Add(1)
		go s.run()
	})
}

func (s *TelegramNotificationService) Stop() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		s.cancel()
		s.wg.Wait()
		s.running.Store(false)
	})
}

// PublishGatewayNotification is safe to call from gateway request paths. It
// never touches the database or Telegram HTTP API synchronously.
func (s *TelegramNotificationService) PublishGatewayNotification(event GatewayNotificationEvent) {
	if s == nil || !normalizeGatewayNotificationEvent(&event) {
		return
	}
	select {
	case s.events <- event:
		s.ingressQueued.Add(1)
	default:
		s.ingressDropped.Add(1)
	}
}

func (s *TelegramNotificationService) run() {
	defer s.wg.Done()
	defer s.running.Store(false)

	deliveryTicker := time.NewTicker(telegramNotificationPollInterval)
	defer deliveryTicker.Stop()
	cleanupTicker := time.NewTicker(telegramNotificationCleanupInterval)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case event := <-s.events:
			s.persistEvent(event)
		case <-deliveryTicker.C:
			s.processDeliveryBatch()
		case <-cleanupTicker.C:
			s.cleanupDelivered()
		}
	}
}

func (s *TelegramNotificationService) persistEvent(event GatewayNotificationEvent) {
	snapshot := s.currentConfig()
	if !shouldDeliverTelegramEvent(snapshot.cfg, event.Type) || strings.TrimSpace(snapshot.cfg.BotToken) == "" || len(snapshot.chatIDs) == 0 {
		s.suppressed.Add(1)
		return
	}

	now := time.Now().UTC()
	dedupeWindow := time.Duration(snapshot.cfg.DedupeWindowSeconds) * time.Second
	if dedupeWindow <= 0 {
		dedupeWindow = 5 * time.Minute
	}
	dedupeBucket := now.Unix() / int64(dedupeWindow/time.Second)
	availableAt := now.Add(telegramNotificationBatchingWindow)
	ctx, cancel := context.WithTimeout(s.ctx, telegramNotificationDBTimeout)
	err := s.outbox.Enqueue(ctx, event, telegramNotificationDedupeKey(event), dedupeBucket, availableAt)
	cancel()
	if err != nil {
		s.recordFailure(fmt.Errorf("persist Telegram notification: %w", err))
		return
	}
	s.persisted.Add(1)
}

func (s *TelegramNotificationService) processDeliveryBatch() {
	ctx, cancel := context.WithTimeout(s.ctx, telegramNotificationDBTimeout)
	events, err := s.outbox.Claim(ctx, s.workerID, telegramNotificationBatchSize, telegramNotificationClaimLease)
	cancel()
	if err != nil {
		if s.ctx.Err() == nil {
			s.recordFailure(fmt.Errorf("claim Telegram notifications: %w", err))
		}
		return
	}
	for _, event := range events {
		if s.ctx.Err() != nil {
			return
		}
		s.processDeliveryEvent(event)
	}
}

func (s *TelegramNotificationService) processDeliveryEvent(event TelegramNotificationOutboxEvent) {
	snapshot := s.currentConfig()
	if !shouldDeliverTelegramEvent(snapshot.cfg, event.Event.Type) || strings.TrimSpace(snapshot.cfg.BotToken) == "" || len(snapshot.chatIDs) == 0 {
		s.markDelivered(event)
		s.suppressed.Add(1)
		return
	}
	if !s.rateLimiter.Allow(time.Now()) {
		s.retryEvent(event, time.Minute, errors.New("Telegram notification rate limit reached"))
		return
	}

	message := formatTelegramGatewayNotification(event)
	for _, chatID := range snapshot.chatIDs {
		retryAfter, err := s.sendMessage(s.ctx, snapshot.cfg.BotToken, chatID, snapshot.cfg.ThreadID, message)
		if err != nil {
			delay := telegramNotificationRetryDelay(event.Attempts + 1)
			if retryAfter > delay {
				delay = retryAfter
			}
			s.retryEvent(event, delay, err)
			return
		}
	}
	s.markDelivered(event)
}

func (s *TelegramNotificationService) markDelivered(event TelegramNotificationOutboxEvent) {
	ctx, cancel := context.WithTimeout(s.ctx, telegramNotificationDBTimeout)
	err := s.outbox.MarkDelivered(ctx, event.ID, s.workerID)
	cancel()
	if err != nil {
		s.recordFailure(fmt.Errorf("ack Telegram notification %d: %w", event.ID, err))
		return
	}
	s.delivered.Add(1)
	s.lastError.Store("")
}

func (s *TelegramNotificationService) retryEvent(event TelegramNotificationOutboxEvent, delay time.Duration, err error) {
	if delay < time.Second {
		delay = time.Second
	}
	ctx, cancel := context.WithTimeout(s.ctx, telegramNotificationDBTimeout)
	retryErr := s.outbox.RetryClaimed(ctx, event.ID, s.workerID, time.Now().UTC().Add(delay), boundedTelegramNotificationError(err))
	cancel()
	if retryErr != nil {
		s.recordFailure(fmt.Errorf("retry Telegram notification %d: %w", event.ID, retryErr))
		return
	}
	s.recordFailure(err)
}

func (s *TelegramNotificationService) cleanupDelivered() {
	ctx, cancel := context.WithTimeout(s.ctx, telegramNotificationDBTimeout)
	_, err := s.outbox.DeleteDeliveredBefore(ctx, time.Now().UTC().Add(-telegramNotificationRetention), 500)
	cancel()
	if err != nil && s.ctx.Err() == nil {
		s.recordFailure(fmt.Errorf("clean Telegram notifications: %w", err))
	}
}

// SetGatewayNotificationPublisher is configured during application wiring.
// Atomic publication allows gateway handlers to read the dependency without a
// lock while retaining a test-friendly setter.
func (s *OpenAIGatewayService) SetGatewayNotificationPublisher(publisher GatewayNotificationPublisher) {
	if s == nil {
		return
	}
	s.gatewayNotificationPublisher.Store(&gatewayNotificationPublisherHolder{publisher: publisher})
}

func (s *OpenAIGatewayService) publishGatewayNotification(event GatewayNotificationEvent) {
	if s == nil {
		return
	}
	holder := s.gatewayNotificationPublisher.Load()
	if holder == nil || holder.publisher == nil {
		return
	}
	holder.publisher.PublishGatewayNotification(event)
}

func (s *TelegramNotificationService) currentConfig() *telegramNotificationConfigSnapshot {
	if s != nil {
		if snapshot := s.config.Load(); snapshot != nil {
			return snapshot
		}
	}
	return telegramNotificationConfigSnapshotFromConfig(nil)
}

func telegramNotificationConfigSnapshotFromConfig(cfg *config.Config) *telegramNotificationConfigSnapshot {
	telegramCfg := config.TelegramNotificationConfig{}
	if cfg != nil {
		telegramCfg = cfg.Notifications.Telegram
	}
	telegramCfg.BotToken = strings.TrimSpace(telegramCfg.BotToken)
	if telegramCfg.DedupeWindowSeconds <= 0 {
		telegramCfg.DedupeWindowSeconds = 300
	}
	if telegramCfg.QueueSize <= 0 {
		telegramCfg.QueueSize = telegramNotificationIngressQueueSize
	}
	if telegramCfg.RequestTimeoutSeconds <= 0 {
		telegramCfg.RequestTimeoutSeconds = int(telegramNotificationRequestTimeout / time.Second)
	}
	chatIDs := normalizeTelegramChatIDs(telegramCfg.ChatID)
	if telegramCfg.Enabled && (telegramCfg.BotToken == "" || len(chatIDs) == 0) {
		slog.Warn("Telegram notifications enabled but bot token or chat ID is missing")
	}
	return &telegramNotificationConfigSnapshot{cfg: telegramCfg, chatIDs: chatIDs}
}

func normalizeTelegramChatIDs(raw string) []string {
	seen := make(map[string]struct{})
	chatIDs := make([]string, 0, 1)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !telegramChatIDPattern.MatchString(value) {
			slog.Warn("ignoring invalid Telegram chat ID from configuration")
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		chatIDs = append(chatIDs, value)
	}
	return chatIDs
}

func (s *TelegramNotificationService) sendMessage(ctx context.Context, token, chatID string, threadID int64, message string) (time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(struct {
		ChatID          string `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id,omitempty"`
		Text            string `json:"text"`
	}{ChatID: chatID, MessageThreadID: threadID, Text: message})
	if err != nil {
		return 0, errors.New("encode Telegram notification")
	}
	endpoint := strings.TrimRight(s.telegramAPIBaseURL, "/") + "/bot" + url.PathEscape(token) + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return 0, errors.New("create Telegram API request")
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.httpClient
	if client == nil {
		client = newTelegramNotificationHTTPClient(telegramNotificationRequestTimeout)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New("Telegram API request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal(body, &result)
	if resp.StatusCode == http.StatusTooManyRequests {
		return time.Duration(result.Parameters.RetryAfter) * time.Second, errors.New("Telegram API rate limited the notification")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("Telegram API returned status %d", resp.StatusCode)
	}
	if len(body) > 0 && !result.OK {
		return 0, errors.New("Telegram API rejected the notification")
	}
	return 0, nil
}

func newTelegramNotificationHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = telegramNotificationRequestTimeout
	}
	return &http.Client{
		Timeout: timeout,
		// The bot token is part of Telegram's API path. Do not follow a
		// redirect to an arbitrary location if the upstream is intercepted.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *TelegramNotificationService) recordFailure(err error) {
	if err == nil {
		return
	}
	message := boundedTelegramNotificationError(err)
	s.failures.Add(1)
	s.lastError.Store(message)
	slog.Warn("Telegram notification delivery failed", "error", message)
}

var telegramChatIDPattern = regexp.MustCompile(`^(?:-?[0-9]{1,20}|@[A-Za-z0-9_]{5,64})$`)

func shouldDeliverTelegramEvent(cfg config.TelegramNotificationConfig, eventType GatewayNotificationEventType) bool {
	if !cfg.Enabled {
		return false
	}
	switch eventType {
	case GatewayNotificationEventError:
		return cfg.NotifyErrors
	case GatewayNotificationEventTimeout:
		return cfg.NotifyTimeouts
	case GatewayNotificationEventSwitch:
		return cfg.NotifyAccountSwitches
	default:
		return false
	}
}

func normalizeGatewayNotificationEvent(event *GatewayNotificationEvent) bool {
	if event == nil {
		return false
	}
	switch event.Type {
	case GatewayNotificationEventError, GatewayNotificationEventTimeout, GatewayNotificationEventSwitch:
	default:
		return false
	}
	event.Platform = boundedTelegramField(event.Platform, 64)
	event.UserName = boundedTelegramField(event.UserName, 128)
	event.AccountName = boundedTelegramField(event.AccountName, 128)
	event.FromAccountName = boundedTelegramField(event.FromAccountName, 128)
	event.ToAccountName = boundedTelegramField(event.ToAccountName, 128)
	event.Model = boundedTelegramField(event.Model, 160)
	event.Stage = boundedTelegramField(event.Stage, 64)
	event.Reason = boundedTelegramField(event.Reason, 300)
	if event.StatusCode < 0 || event.StatusCode > 999 {
		event.StatusCode = 0
	}
	if event.ElapsedMs < 0 {
		event.ElapsedMs = 0
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	return true
}

func boundedTelegramField(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func telegramNotificationDedupeKey(event GatewayNotificationEvent) string {
	hash := sha256.New()
	for _, value := range []string{
		string(event.Type),
		event.Platform,
		fmt.Sprintf("%d", event.UserID),
		event.UserName,
		fmt.Sprintf("%d", event.AccountID),
		event.AccountName,
		fmt.Sprintf("%d", event.FromAccountID),
		event.FromAccountName,
		fmt.Sprintf("%d", event.ToAccountID),
		event.ToAccountName,
		event.Model,
		fmt.Sprintf("%d", event.StatusCode),
		event.Stage,
		event.Reason,
		fmt.Sprintf("%d", event.FromPriority),
		fmt.Sprintf("%d", event.ToPriority),
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func formatTelegramGatewayNotification(event TelegramNotificationOutboxEvent) string {
	var message string
	switch event.Event.Type {
	case GatewayNotificationEventError:
		lines := []string{"❌ ERROR"}
		if event.Event.StatusCode > 0 {
			lines[0] += " " + strconv.Itoa(event.Event.StatusCode)
		}
		lines = appendTelegramEventDetails(lines, event, true, false)
		message = strings.Join(lines, "\n")
	case GatewayNotificationEventTimeout:
		lines := []string{"⚠️ TIMEOUT " + formatTelegramElapsed(event.Event.ElapsedMs)}
		lines = appendTelegramEventDetails(lines, event, true, true)
		message = strings.Join(lines, "\n")
	case GatewayNotificationEventSwitch:
		from := telegramAccountLabel(event.Event.FromAccountName, event.Event.FromAccountID, event.Event.AccountName, event.Event.AccountID)
		to := telegramAccountLabel(event.Event.ToAccountName, event.Event.ToAccountID, "", 0)
		icon := "✅"
		if event.Event.ToPriority < event.Event.FromPriority {
			icon = "❤️"
		}
		lines := []string{
			fmt.Sprintf("%s account %s -> account %s", icon, from, to),
			fmt.Sprintf("From: %s; priority %d", telegramDetailedAccountLabel(event.Event.FromAccountName, event.Event.FromAccountID), event.Event.FromPriority),
			fmt.Sprintf("To: %s; priority %d", telegramDetailedAccountLabel(event.Event.ToAccountName, event.Event.ToAccountID), event.Event.ToPriority),
		}
		lines = appendTelegramEventDetails(lines, event, false, true)
		message = strings.Join(lines, "\n")
	default:
		message = "[sub2api] " + string(event.Event.Type)
	}
	if len(message) > telegramNotificationMaxMessageBytes {
		return message[:telegramNotificationMaxMessageBytes]
	}
	return message
}

func appendTelegramEventDetails(lines []string, event TelegramNotificationOutboxEvent, includeAccount, includeStatus bool) []string {
	if event.Event.Platform != "" {
		lines = append(lines, "Platform: "+event.Event.Platform)
	}
	if user := telegramDetailedUserLabel(event.Event.UserName, event.Event.UserID); user != "" {
		lines = append(lines, "User: "+user)
	}
	if includeAccount {
		if account := telegramDetailedAccountLabel(event.Event.AccountName, event.Event.AccountID); account != "" {
			lines = append(lines, "Account: "+account)
		}
	}
	if event.Event.Model != "" {
		lines = append(lines, "Model: "+event.Event.Model)
	}
	if includeStatus && event.Event.StatusCode > 0 {
		lines = append(lines, "Status: "+strconv.Itoa(event.Event.StatusCode))
	}
	if event.Event.Stage != "" {
		lines = append(lines, "Stage: "+event.Event.Stage)
	}
	if event.Event.Reason != "" {
		lines = append(lines, "Reason: "+event.Event.Reason)
	}
	if event.OccurrenceCount > 1 {
		lines = append(lines, fmt.Sprintf("Occurrences: %d", event.OccurrenceCount))
	}
	if at := telegramNotificationOccurredAt(event); !at.IsZero() {
		lines = append(lines, "Last seen: "+at.UTC().Format(time.RFC3339))
	}
	return lines
}

func telegramDetailedUserLabel(name string, id int64) string {
	return telegramDetailedEntityLabel(name, id)
}

func telegramDetailedAccountLabel(name string, id int64) string {
	return telegramDetailedEntityLabel(name, id)
}

func telegramDetailedEntityLabel(name string, id int64) string {
	name = strings.TrimSpace(name)
	if name != "" && id > 0 {
		return fmt.Sprintf("%s (%d)", name, id)
	}
	if name != "" {
		return name
	}
	if id > 0 {
		return strconv.FormatInt(id, 10)
	}
	return ""
}

func telegramNotificationOccurredAt(event TelegramNotificationOutboxEvent) time.Time {
	if !event.LastOccurredAt.IsZero() {
		return event.LastOccurredAt
	}
	return event.Event.OccurredAt
}

func telegramAccountLabel(name string, id int64, fallbackName string, fallbackID int64) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	if id > 0 {
		return strconv.FormatInt(id, 10)
	}
	if fallbackName = strings.TrimSpace(fallbackName); fallbackName != "" {
		return fallbackName
	}
	if fallbackID > 0 {
		return strconv.FormatInt(fallbackID, 10)
	}
	return "unknown"
}

func formatTelegramElapsed(elapsedMs int64) string {
	if elapsedMs <= 0 {
		return "unknown"
	}
	if elapsedMs < 1000 {
		return fmt.Sprintf("%dms", elapsedMs)
	}
	return fmt.Sprintf("%.1fs", float64(elapsedMs)/1000)
}

func telegramNotificationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 9 {
		attempt = 9
	}
	delay := time.Second * time.Duration(1<<(attempt-1))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func boundedTelegramNotificationError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 512 {
		return message[:512]
	}
	return message
}

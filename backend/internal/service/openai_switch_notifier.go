package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const defaultOpenAISwitchNotifierTelegramBaseURL = "https://api.telegram.org"

const (
	OpenAIAccountSwitchPhaseStarted   = "started"
	OpenAIAccountSwitchPhaseCompleted = "completed"
	OpenAIAccountSwitchPhaseFailed    = "failed"
	OpenAIAccountSwitchPhaseCancelled = "cancelled"
)

// OpenAIAccountSwitchNotification describes an OpenAI upstream account switch.
type OpenAIAccountSwitchNotification struct {
	EventName         string
	Phase             string
	OccurredAt        time.Time
	Route             string
	RequestID         string
	ClientRequestID   string
	Path              string
	Method            string
	UserID            int64
	UserName          string
	APIKeyID          int64
	APIKeyName        string
	GroupID           string
	GroupName         string
	Model             string
	Stream            bool
	AccountID         int64
	AccountName       string
	FailedAccountID   int64
	FailedAccountName string
	TargetAccountID   int64
	TargetAccountName string
	UpstreamStatus    int
	FinalStatus       int
	FinalError        string
	LatencyMs         int64
	SwitchCount       int
	MaxSwitches       int
}

// OpenAIAccountSwitchNotifier sends account-switch notifications.
type OpenAIAccountSwitchNotifier struct {
	telegramEnabled bool
	botToken        string
	chatID          string
	apiBaseURL      string
	httpClient      *http.Client
	timeout         time.Duration
	minInterval     time.Duration
	now             func() time.Time

	mu       sync.Mutex
	lastSent map[string]time.Time
}

// NewOpenAIAccountSwitchNotifier creates a notifier from runtime config.
func NewOpenAIAccountSwitchNotifier(cfg *config.Config) *OpenAIAccountSwitchNotifier {
	if cfg == nil {
		return nil
	}
	tg := cfg.Gateway.OpenAISwitchNotify.Telegram
	if !tg.Enabled {
		return nil
	}
	if strings.TrimSpace(tg.BotToken) == "" || strings.TrimSpace(tg.ChatID) == "" {
		slog.Warn("openai switch Telegram notification enabled but bot token or chat id is empty")
		return nil
	}

	timeout := time.Duration(tg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	minInterval := time.Duration(cfg.Gateway.OpenAISwitchNotify.MinIntervalSeconds) * time.Second
	if minInterval < 0 {
		minInterval = 0
	}

	return &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        strings.TrimSpace(tg.BotToken),
		chatID:          strings.TrimSpace(tg.ChatID),
		apiBaseURL:      defaultOpenAISwitchNotifierTelegramBaseURL,
		httpClient:      &http.Client{Timeout: timeout},
		timeout:         timeout,
		minInterval:     minInterval,
		now:             time.Now,
		lastSent:        make(map[string]time.Time),
	}
}

// NotifyAsync sends the notification in the background. Failures are logged and
// never returned to the request path.
func (n *OpenAIAccountSwitchNotifier) NotifyAsync(event OpenAIAccountSwitchNotification) {
	if n == nil || !n.telegramEnabled {
		return
	}
	if !n.markSend(event) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
		defer cancel()
		if err := n.sendTelegram(ctx, event); err != nil {
			slog.Warn("openai switch Telegram notification failed",
				"event", event.EventName,
				"route", event.Route,
				"account_id", event.AccountID,
				"upstream_status", event.UpstreamStatus,
				"err", err,
			)
		}
	}()
}

// Notify sends synchronously. It is intended for tests and direct callers.
func (n *OpenAIAccountSwitchNotifier) Notify(ctx context.Context, event OpenAIAccountSwitchNotification) error {
	if n == nil || !n.telegramEnabled {
		return nil
	}
	if !n.markSend(event) {
		return nil
	}
	return n.sendTelegram(ctx, event)
}

func (n *OpenAIAccountSwitchNotifier) markSend(event OpenAIAccountSwitchNotification) bool {
	if n.minInterval <= 0 {
		return true
	}
	key := event.dedupeKey()
	now := n.now()
	n.mu.Lock()
	defer n.mu.Unlock()
	if last, ok := n.lastSent[key]; ok && now.Sub(last) < n.minInterval {
		return false
	}
	n.lastSent[key] = now
	return true
}

func (n *OpenAIAccountSwitchNotifier) sendTelegram(ctx context.Context, event OpenAIAccountSwitchNotification) error {
	client := n.httpClient
	if client == nil {
		client = &http.Client{Timeout: n.timeout}
	}
	payload := map[string]any{
		"chat_id":                  telegramChatIDValue(n.chatID),
		"text":                     event.telegramText(),
		"disable_web_page_preview": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Telegram payload: %w", err)
	}
	url := strings.TrimRight(n.apiBaseURL, "/") + "/bot" + n.botToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Telegram request: %s", redactTelegramToken(err.Error(), n.botToken))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send Telegram request: %s", redactTelegramToken(err.Error(), n.botToken))
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("telegram returned status %d: %s", resp.StatusCode, redactTelegramToken(strings.TrimSpace(string(respBody)), n.botToken))
	}
	return nil
}

func redactTelegramToken(message, token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return message
	}
	return strings.ReplaceAll(message, token, "<redacted>")
}

func telegramChatIDValue(chatID string) any {
	trimmed := strings.TrimSpace(chatID)
	if i, err := strconv.ParseInt(trimmed, 10, 64); err == nil && strconv.FormatInt(i, 10) == trimmed {
		return i
	}
	return trimmed
}

func (e OpenAIAccountSwitchNotification) dedupeKey() string {
	return strings.Join([]string{
		e.phase(),
		e.EventName,
		e.Route,
		strconv.FormatInt(e.failedAccountID(), 10),
		strconv.FormatInt(e.TargetAccountID, 10),
		strconv.Itoa(e.UpstreamStatus),
		strconv.Itoa(e.FinalStatus),
		e.Model,
	}, "|")
}

func (e OpenAIAccountSwitchNotification) telegramText() string {
	eventName := strings.TrimSpace(e.EventName)
	if eventName == "" {
		eventName = "openai.upstream_failover_switching"
	}
	var b strings.Builder
	_, _ = b.WriteString(e.telegramTitle())
	writeNotificationLine(&b, "event", eventName)
	writeNotificationLine(&b, "time", e.eventTime().Format("2006-01-02 15:04:05 -0700"))
	writeNotificationLine(&b, "route", e.Route)
	writeNotificationLine(&b, "model", e.Model)
	switch e.phase() {
	case OpenAIAccountSwitchPhaseCompleted:
		writeNotificationLine(&b, "from", displayNameID(e.failedAccountName(), e.failedAccountID()))
		writeNotificationLine(&b, "to", displayNameID(e.TargetAccountName, e.TargetAccountID))
		writeNotificationLine(&b, "final status", strconv.Itoa(e.FinalStatus))
	case OpenAIAccountSwitchPhaseFailed:
		writeNotificationLine(&b, "from", displayNameID(e.failedAccountName(), e.failedAccountID()))
		writeNotificationLine(&b, "final status", strconv.Itoa(e.FinalStatus))
		writeNotificationLine(&b, "reason", e.FinalError)
	case OpenAIAccountSwitchPhaseCancelled:
		writeNotificationLine(&b, "from", displayNameID(e.failedAccountName(), e.failedAccountID()))
		writeNotificationLine(&b, "to", displayNameID(e.TargetAccountName, e.TargetAccountID))
		writeNotificationLine(&b, "reason", e.FinalError)
	default:
		writeNotificationLine(&b, "failed account", displayNameID(e.failedAccountName(), e.failedAccountID()))
		writeNotificationLine(&b, "status", strconv.Itoa(e.UpstreamStatus))
	}
	if e.SwitchCount > 0 || e.MaxSwitches > 0 {
		writeNotificationLine(&b, "switch", fmt.Sprintf("%d/%d", e.SwitchCount, e.MaxSwitches))
	}
	if e.LatencyMs > 0 {
		writeNotificationLine(&b, "latency", formatDurationMs(e.LatencyMs))
	}
	writeNotificationLine(&b, "user", displayNameID(e.UserName, e.UserID))
	writeNotificationLine(&b, "api key", displayNameID(e.APIKeyName, e.APIKeyID))
	writeNotificationLine(&b, "group", displayNameID(e.GroupName, parseNotificationGroupID(e.GroupID)))
	writeNotificationLine(&b, "request_id", e.RequestID)
	writeNotificationLine(&b, "client_request_id", e.ClientRequestID)
	if strings.TrimSpace(e.Method) != "" || strings.TrimSpace(e.Path) != "" {
		writeNotificationLine(&b, "request", strings.TrimSpace(e.Method+" "+e.Path))
	}
	writeNotificationLine(&b, "stream", strconv.FormatBool(e.Stream))
	return b.String()
}

func (e OpenAIAccountSwitchNotification) phase() string {
	switch strings.TrimSpace(e.Phase) {
	case OpenAIAccountSwitchPhaseCompleted:
		return OpenAIAccountSwitchPhaseCompleted
	case OpenAIAccountSwitchPhaseFailed:
		return OpenAIAccountSwitchPhaseFailed
	case OpenAIAccountSwitchPhaseCancelled:
		return OpenAIAccountSwitchPhaseCancelled
	default:
		return OpenAIAccountSwitchPhaseStarted
	}
}

func (e OpenAIAccountSwitchNotification) telegramTitle() string {
	switch e.phase() {
	case OpenAIAccountSwitchPhaseCompleted:
		return "sub2api OpenAI failover completed\n"
	case OpenAIAccountSwitchPhaseFailed:
		return "sub2api OpenAI failover failed\n"
	case OpenAIAccountSwitchPhaseCancelled:
		return "sub2api OpenAI failover cancelled\n"
	default:
		return "sub2api OpenAI failover started\n"
	}
}

func (e OpenAIAccountSwitchNotification) eventTime() time.Time {
	if e.OccurredAt.IsZero() {
		return time.Now()
	}
	return e.OccurredAt
}

func (e OpenAIAccountSwitchNotification) failedAccountID() int64 {
	if e.FailedAccountID > 0 {
		return e.FailedAccountID
	}
	return e.AccountID
}

func (e OpenAIAccountSwitchNotification) failedAccountName() string {
	if strings.TrimSpace(e.FailedAccountName) != "" {
		return e.FailedAccountName
	}
	return e.AccountName
}

func displayNameID(name string, id int64) string {
	name = strings.TrimSpace(name)
	switch {
	case name != "" && id > 0:
		return fmt.Sprintf("%s (#%d)", name, id)
	case id > 0:
		return fmt.Sprintf("#%d", id)
	default:
		return name
	}
}

func parseNotificationGroupID(groupID string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(groupID), 10, 64)
	return parsed
}

func formatDurationMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func writeNotificationLine(b *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	_, _ = b.WriteString(key)
	_, _ = b.WriteString(": ")
	_, _ = b.WriteString(value)
	_ = b.WriteByte('\n')
}

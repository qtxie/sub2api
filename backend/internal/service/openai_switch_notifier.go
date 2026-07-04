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

// OpenAIAccountSwitchNotification describes an OpenAI upstream account switch.
type OpenAIAccountSwitchNotification struct {
	EventName       string
	Route           string
	RequestID       string
	ClientRequestID string
	Path            string
	Method          string
	UserID          int64
	APIKeyID        int64
	GroupID         string
	Model           string
	Stream          bool
	AccountID       int64
	UpstreamStatus  int
	SwitchCount     int
	MaxSwitches     int
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
		e.EventName,
		e.Route,
		strconv.FormatInt(e.AccountID, 10),
		strconv.Itoa(e.UpstreamStatus),
		e.Model,
	}, "|")
}

func (e OpenAIAccountSwitchNotification) telegramText() string {
	eventName := strings.TrimSpace(e.EventName)
	if eventName == "" {
		eventName = "openai.upstream_failover_switching"
	}
	var b strings.Builder
	_, _ = b.WriteString("sub2api OpenAI account switch\n")
	writeNotificationLine(&b, "event", eventName)
	writeNotificationLine(&b, "route", e.Route)
	writeNotificationLine(&b, "model", e.Model)
	writeNotificationLine(&b, "account_id", strconv.FormatInt(e.AccountID, 10))
	writeNotificationLine(&b, "upstream_status", strconv.Itoa(e.UpstreamStatus))
	writeNotificationLine(&b, "switch_count", fmt.Sprintf("%d/%d", e.SwitchCount, e.MaxSwitches))
	writeNotificationLine(&b, "request_id", e.RequestID)
	writeNotificationLine(&b, "client_request_id", e.ClientRequestID)
	if e.UserID != 0 {
		writeNotificationLine(&b, "user_id", strconv.FormatInt(e.UserID, 10))
	}
	if e.APIKeyID != 0 {
		writeNotificationLine(&b, "api_key_id", strconv.FormatInt(e.APIKeyID, 10))
	}
	writeNotificationLine(&b, "group_id", e.GroupID)
	if strings.TrimSpace(e.Method) != "" || strings.TrimSpace(e.Path) != "" {
		writeNotificationLine(&b, "request", strings.TrimSpace(e.Method+" "+e.Path))
	}
	writeNotificationLine(&b, "stream", strconv.FormatBool(e.Stream))
	return b.String()
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

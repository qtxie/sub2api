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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const defaultOpenAISwitchNotifierTelegramBaseURL = "https://api.telegram.org"

const (
	openAISwitchNotifierQueueSize      = 1024
	openAISwitchNotifierMaxAttempts    = 3
	openAISwitchNotifierRetryBaseDelay = 250 * time.Millisecond
	telegramSendMessageMaxRunes        = 4096
	telegramSendMessageTruncatedSuffix = "\n...[truncated]"
)

const (
	OpenAIAccountSwitchPhaseStarted   = "started"
	OpenAIAccountSwitchPhaseCompleted = "completed"
	OpenAIAccountSwitchPhaseFailed    = "failed"
	OpenAIAccountSwitchPhaseCancelled = "cancelled"
	OpenAIAccountSwitchPhaseFailback  = "failback"
)

// OpenAIAccountSwitchNotification describes an OpenAI upstream account switch.
type OpenAIAccountSwitchNotification struct {
	EventName             string
	Phase                 string
	OccurredAt            time.Time
	Route                 string
	RequestID             string
	ClientRequestID       string
	Path                  string
	Method                string
	UserID                int64
	UserName              string
	APIKeyID              int64
	APIKeyName            string
	GroupID               string
	GroupName             string
	Model                 string
	Stream                bool
	AccountID             int64
	AccountName           string
	AccountPriority       int
	FailedAccountID       int64
	FailedAccountName     string
	FailedPriority        int
	TargetAccountID       int64
	TargetAccountName     string
	TargetPriority        int
	UpstreamStatus        int
	FinalStatus           int
	FinalError            string
	ClientStatus          int
	StreamStarted         bool
	FallbackWritten       bool
	UpstreamWritten       bool
	LatencyMs             int64
	AttemptLatencyMs      int64
	SwitchLatencyMs       int64
	TotalRequestLatencyMs int64
	BudgetRemainingMs     int64
	ClientConnected       bool
	TransportStarted      bool
	SemanticStarted       bool
	SwitchCount           int
	MaxSwitches           int
}

// OpenAIAccountSwitchNotifier sends account-switch notifications.
type OpenAIAccountSwitchNotifier struct {
	telegramEnabled bool
	sendStarted     bool
	botToken        string
	chatID          string
	apiBaseURL      string
	httpClient      *http.Client
	timeout         time.Duration
	minInterval     time.Duration
	retryBaseDelay  time.Duration
	now             func() time.Time
	sleep           func(context.Context, time.Duration) error

	mu        sync.Mutex
	lastSent  map[string]time.Time
	inFlight  map[string]struct{}
	queueMu   sync.RWMutex
	queue     chan OpenAIAccountSwitchNotification
	closed    bool
	workerWG  sync.WaitGroup
	workerCtx context.Context
	cancel    context.CancelFunc
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

	n := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		sendStarted:     cfg.Gateway.OpenAISwitchNotify.SendStarted,
		botToken:        strings.TrimSpace(tg.BotToken),
		chatID:          strings.TrimSpace(tg.ChatID),
		apiBaseURL:      defaultOpenAISwitchNotifierTelegramBaseURL,
		httpClient:      &http.Client{Timeout: timeout},
		timeout:         timeout,
		minInterval:     minInterval,
		retryBaseDelay:  openAISwitchNotifierRetryBaseDelay,
		now:             time.Now,
		sleep:           sleepOpenAISwitchNotifier,
		lastSent:        make(map[string]time.Time),
		inFlight:        make(map[string]struct{}),
		queue:           make(chan OpenAIAccountSwitchNotification, openAISwitchNotifierQueueSize),
	}
	n.workerCtx, n.cancel = context.WithCancel(context.Background())
	n.workerWG.Add(1)
	go n.run()
	return n
}

// NotifyAsync sends the notification in the background. Failures are logged and
// never returned to the request path.
func (n *OpenAIAccountSwitchNotifier) NotifyAsync(event OpenAIAccountSwitchNotification) {
	if n == nil || !n.telegramEnabled {
		return
	}
	if !n.shouldSend(event) {
		return
	}
	n.queueMu.RLock()
	defer n.queueMu.RUnlock()
	if n.closed || n.queue == nil {
		return
	}
	select {
	case n.queue <- event:
	default:
		slog.Error("openai switch Telegram notification queue full",
			"event", event.EventName,
			"route", event.Route,
			"account_id", event.AccountID,
			"queue_capacity", cap(n.queue),
		)
	}
}

// Notify sends synchronously. It is intended for tests and direct callers.
func (n *OpenAIAccountSwitchNotifier) Notify(ctx context.Context, event OpenAIAccountSwitchNotification) error {
	if n == nil || !n.telegramEnabled {
		return nil
	}
	if !n.shouldSend(event) {
		return nil
	}
	if !n.reserveSend(event) {
		return nil
	}
	err := n.sendTelegramWithRetry(ctx, event)
	n.finishSend(event, err == nil)
	return err
}

func (n *OpenAIAccountSwitchNotifier) shouldSend(event OpenAIAccountSwitchNotification) bool {
	if event.phase() == OpenAIAccountSwitchPhaseStarted && !n.sendStarted {
		return false
	}
	return true
}

func (n *OpenAIAccountSwitchNotifier) reserveSend(event OpenAIAccountSwitchNotification) bool {
	if n.minInterval <= 0 {
		return true
	}
	key := event.dedupeKey()
	now := n.currentTime()
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.inFlight[key]; ok {
		return false
	}
	if last, ok := n.lastSent[key]; ok && now.Sub(last) < n.minInterval {
		return false
	}
	if n.inFlight == nil {
		n.inFlight = make(map[string]struct{})
	}
	n.inFlight[key] = struct{}{}
	return true
}

func (n *OpenAIAccountSwitchNotifier) finishSend(event OpenAIAccountSwitchNotification, sent bool) {
	if n.minInterval <= 0 {
		return
	}
	key := event.dedupeKey()
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.inFlight, key)
	if sent {
		if n.lastSent == nil {
			n.lastSent = make(map[string]time.Time)
		}
		n.lastSent[key] = n.currentTime()
	}
}

func (n *OpenAIAccountSwitchNotifier) currentTime() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

func (n *OpenAIAccountSwitchNotifier) run() {
	defer n.workerWG.Done()
	for event := range n.queue {
		if !n.reserveSend(event) {
			continue
		}
		err := n.sendTelegramWithRetry(n.workerCtx, event)
		n.finishSend(event, err == nil)
		if err != nil {
			slog.Warn("openai switch Telegram notification failed",
				"event", event.EventName,
				"route", event.Route,
				"account_id", event.AccountID,
				"upstream_status", event.UpstreamStatus,
				"err", err,
			)
		}
	}
}

// Close stops accepting notifications and waits for queued deliveries to finish.
func (n *OpenAIAccountSwitchNotifier) Close(ctx context.Context) error {
	if n == nil || n.queue == nil {
		return nil
	}
	n.queueMu.Lock()
	if !n.closed {
		n.closed = true
		close(n.queue)
	}
	n.queueMu.Unlock()

	done := make(chan struct{})
	go func() {
		n.workerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		if n.cancel != nil {
			n.cancel()
		}
		return nil
	case <-ctx.Done():
		if n.cancel != nil {
			n.cancel()
		}
		return ctx.Err()
	}
}

func (n *OpenAIAccountSwitchNotifier) sendTelegram(ctx context.Context, event OpenAIAccountSwitchNotification) error {
	client := n.httpClient
	if client == nil {
		client = &http.Client{Timeout: n.timeout}
	}
	payload := map[string]any{
		"chat_id":                  telegramChatIDValue(n.chatID),
		"text":                     truncateTelegramText(event.telegramText()),
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
		return &telegramDeliveryError{
			err:       fmt.Errorf("send Telegram request: %s", redactTelegramToken(err.Error(), n.botToken)),
			retryable: true,
		}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if readErr != nil {
		return &telegramDeliveryError{err: fmt.Errorf("read Telegram response: %w", readErr), retryable: true}
	}
	var result telegramAPIResponse
	parseErr := json.Unmarshal(respBody, &result)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && parseErr == nil && result.OK {
		return nil
	}

	retryAfter := time.Duration(result.Parameters.RetryAfter) * time.Second
	retryable := parseErr != nil || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError || result.ErrorCode == http.StatusTooManyRequests || result.ErrorCode >= http.StatusInternalServerError
	detail := strings.TrimSpace(result.Description)
	if detail == "" {
		detail = strings.TrimSpace(string(respBody))
	}
	if parseErr != nil && detail == "" {
		detail = parseErr.Error()
	}
	return &telegramDeliveryError{
		err:        fmt.Errorf("telegram returned status %d: %s", resp.StatusCode, redactTelegramToken(detail, n.botToken)),
		retryable:  retryable,
		retryAfter: retryAfter,
	}
}

type telegramAPIResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type telegramDeliveryError struct {
	err        error
	retryable  bool
	retryAfter time.Duration
}

func (e *telegramDeliveryError) Error() string {
	if e == nil || e.err == nil {
		return "Telegram delivery failed"
	}
	return e.err.Error()
}

func (e *telegramDeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (n *OpenAIAccountSwitchNotifier) sendTelegramWithRetry(ctx context.Context, event OpenAIAccountSwitchNotification) error {
	var lastErr error
	for attempt := 1; attempt <= openAISwitchNotifierMaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, n.timeout)
		err := n.sendTelegram(attemptCtx, event)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		var deliveryErr *telegramDeliveryError
		if attempt == openAISwitchNotifierMaxAttempts || !errors.As(err, &deliveryErr) || !deliveryErr.retryable {
			break
		}
		delay := deliveryErr.retryAfter
		if delay <= 0 {
			delay = n.retryBaseDelay * time.Duration(1<<(attempt-1))
		}
		if err := n.wait(ctx, delay); err != nil {
			return fmt.Errorf("Telegram retry interrupted: %w", err)
		}
	}
	return lastErr
}

func (n *OpenAIAccountSwitchNotifier) wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if n.sleep != nil {
		return n.sleep(ctx, delay)
	}
	return sleepOpenAISwitchNotifier(ctx, delay)
}

func sleepOpenAISwitchNotifier(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func truncateTelegramText(text string) string {
	runes := []rune(text)
	if len(runes) <= telegramSendMessageMaxRunes {
		return text
	}
	suffix := []rune(telegramSendMessageTruncatedSuffix)
	return string(runes[:telegramSendMessageMaxRunes-len(suffix)]) + telegramSendMessageTruncatedSuffix
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
		e.RequestID,
		e.ClientRequestID,
		strconv.FormatInt(e.UserID, 10),
		strconv.FormatInt(e.APIKeyID, 10),
		strconv.FormatInt(e.OccurredAt.UnixNano(), 10),
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
		writeNotificationLine(&b, "from", displayAccountNameIDPriority(e.failedAccountName(), e.failedAccountID(), e.failedPriority()))
		writeNotificationLine(&b, "to", displayAccountNameIDPriority(e.TargetAccountName, e.TargetAccountID, e.TargetPriority))
		status := e.UpstreamStatus
		if status <= 0 {
			status = e.FinalStatus
		}
		writeNotificationLine(&b, "error status", strconv.Itoa(status))
	case OpenAIAccountSwitchPhaseFailed:
		writeNotificationLine(&b, "from", displayAccountNameIDPriority(e.failedAccountName(), e.failedAccountID(), e.failedPriority()))
		writeNotificationLine(&b, "attempted", displayAccountNameIDPriority(e.TargetAccountName, e.TargetAccountID, e.TargetPriority))
		writeNotificationLine(&b, "final status", strconv.Itoa(e.FinalStatus))
		if e.ClientStatus > 0 {
			clientStatus := strconv.Itoa(e.ClientStatus)
			if e.TransportStarted && !e.SemanticStarted && e.ClientStatus == http.StatusOK {
				clientStatus += " (SSE heartbeat committed)"
			}
			writeNotificationLine(&b, "client status", clientStatus)
		}
		if e.StreamStarted || e.FallbackWritten || e.UpstreamWritten {
			writeNotificationLine(&b, "stream started", strconv.FormatBool(e.StreamStarted))
			writeNotificationLine(&b, "fallback response written", strconv.FormatBool(e.FallbackWritten))
			writeNotificationLine(&b, "upstream response already written", strconv.FormatBool(e.UpstreamWritten))
			if e.StreamStarted || e.UpstreamWritten {
				writeNotificationLine(&b, "retry possible", "false")
			}
		}
		writeNotificationLine(&b, "reason", e.FinalError)
	case OpenAIAccountSwitchPhaseCancelled:
		writeNotificationLine(&b, "from", displayAccountNameIDPriority(e.failedAccountName(), e.failedAccountID(), e.failedPriority()))
		writeNotificationLine(&b, "to", displayAccountNameIDPriority(e.TargetAccountName, e.TargetAccountID, e.TargetPriority))
		writeNotificationLine(&b, "reason", e.FinalError)
	case OpenAIAccountSwitchPhaseFailback:
		writeNotificationLine(&b, "from", displayAccountNameIDPriority(e.failedAccountName(), e.failedAccountID(), e.failedPriority()))
		writeNotificationLine(&b, "to", displayAccountNameIDPriority(e.TargetAccountName, e.TargetAccountID, e.TargetPriority))
		writeNotificationLine(&b, "reason", e.FinalError)
	default:
		writeNotificationLine(&b, "failed account", displayAccountNameIDPriority(e.failedAccountName(), e.failedAccountID(), e.failedPriority()))
		writeNotificationLine(&b, "status", strconv.Itoa(e.UpstreamStatus))
	}
	if e.MaxSwitches > 0 {
		writeNotificationLine(&b, "switch", fmt.Sprintf("%d/%d", e.SwitchCount, e.MaxSwitches))
	} else if e.SwitchCount > 0 {
		writeNotificationLine(&b, "switch", strconv.Itoa(e.SwitchCount))
	}
	if e.LatencyMs > 0 && e.AttemptLatencyMs <= 0 && e.SwitchLatencyMs <= 0 && e.TotalRequestLatencyMs <= 0 {
		writeNotificationLine(&b, "latency", formatDurationMs(e.LatencyMs))
	}
	writeNotificationLine(&b, "attempt_latency", formatDurationMs(e.AttemptLatencyMs))
	writeNotificationLine(&b, "switch_latency", formatDurationMs(e.SwitchLatencyMs))
	writeNotificationLine(&b, "total_request_latency", formatDurationMs(e.TotalRequestLatencyMs))
	writeNotificationLine(&b, "budget_remaining", formatDurationMs(e.BudgetRemainingMs))
	writeNotificationLine(&b, "client_connected", strconv.FormatBool(e.ClientConnected))
	writeNotificationLine(&b, "transport_started", strconv.FormatBool(e.TransportStarted))
	writeNotificationLine(&b, "semantic_started", strconv.FormatBool(e.SemanticStarted))
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
	case OpenAIAccountSwitchPhaseFailback:
		return OpenAIAccountSwitchPhaseFailback
	default:
		return OpenAIAccountSwitchPhaseStarted
	}
}

func (e OpenAIAccountSwitchNotification) telegramTitle() string {
	switch e.phase() {
	case OpenAIAccountSwitchPhaseCompleted:
		return compactSwitchTitle("✅", e.failedAccountName(), e.failedAccountID(), e.TargetAccountName, e.TargetAccountID, "", 0)
	case OpenAIAccountSwitchPhaseFailed:
		status := e.FinalStatus
		if status <= 0 {
			status = e.UpstreamStatus
		}
		if e.semanticStreamStarted() {
			return "⚠️ OpenAI stream failed after start\n"
		}
		return compactSwitchTitle("❌", "", 0, e.failedAccountName(), e.failedAccountID(), e.Model, status)
	case OpenAIAccountSwitchPhaseCancelled:
		return compactSwitchTitle("⚠️", e.failedAccountName(), e.failedAccountID(), e.TargetAccountName, e.TargetAccountID, "", 0)
	case OpenAIAccountSwitchPhaseFailback:
		return compactSwitchTitle("❤️", e.failedAccountName(), e.failedAccountID(), e.TargetAccountName, e.TargetAccountID, "", 0)
	default:
		status := e.UpstreamStatus
		if status <= 0 {
			status = e.FinalStatus
		}
		return compactSwitchTitle("➡️", "", 0, e.failedAccountName(), e.failedAccountID(), e.Model, status)
	}
}

// semanticStreamStarted distinguishes real model output from an SSE heartbeat.
// Pre-output requests expose authoritative transport/semantic state; older routes
// fall back to StreamStarted because they do not populate those fields.
func (e OpenAIAccountSwitchNotification) semanticStreamStarted() bool {
	if e.SemanticStarted {
		return true
	}
	if e.TransportStarted {
		return false
	}
	return e.StreamStarted
}

func compactSwitchTitle(icon, fromName string, fromID int64, toName string, toID int64, model string, status int) string {
	parts := []string{strings.TrimSpace(icon)}
	statusText := ""
	if status > 0 {
		statusText = strconv.Itoa(status)
	}
	model = strings.TrimSpace(model)
	if statusText != "" {
		parts = append(parts, statusText)
	}
	if model != "" {
		parts = append(parts, model)
	}
	from := titleAccountName(fromName, fromID)
	to := titleAccountName(toName, toID)
	switch {
	case from != "" && to != "":
		parts = append(parts, from, "->", to)
	case to != "":
		parts = append(parts, to)
	case from != "":
		parts = append(parts, from)
	}
	if len(parts) == 1 {
		parts = append(parts, "OpenAI")
	}
	return strings.Join(parts, " ") + "\n"
}

func titleAccountName(name string, id int64) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	if id > 0 {
		return fmt.Sprintf("#%d", id)
	}
	return ""
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

func (e OpenAIAccountSwitchNotification) failedPriority() int {
	if e.FailedPriority != 0 {
		return e.FailedPriority
	}
	return e.AccountPriority
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

func displayAccountNameIDPriority(name string, id int64, priority int) string {
	base := displayNameID(name, id)
	if priority <= 0 {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return fmt.Sprintf("priority=%d", priority)
	}
	return fmt.Sprintf("%s priority=%d", base, priority)
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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountSwitchNotifierSendsTelegram(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPayload))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	notifier := NewOpenAIAccountSwitchNotifier(&config.Config{
		Gateway: config.GatewayConfig{
			OpenAISwitchNotify: config.GatewayOpenAISwitchNotifyConfig{
				MinIntervalSeconds: 60,
				SendStarted:        true,
				Telegram: config.GatewayOpenAISwitchNotifyTelegramConfig{
					Enabled:        true,
					BotToken:       "test-token",
					ChatID:         "12345",
					TimeoutSeconds: 5,
				},
			},
		},
	})
	require.NotNil(t, notifier)
	notifier.apiBaseURL = server.URL

	err := notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{
		EventName:         "openai.upstream_failover_switching",
		Phase:             OpenAIAccountSwitchPhaseStarted,
		OccurredAt:        time.Date(2026, 7, 6, 12, 14, 36, 0, time.FixedZone("CST", 8*3600)),
		Route:             "responses",
		RequestID:         "req-1",
		ClientRequestID:   "client-1",
		Path:              "/responses",
		Method:            http.MethodPost,
		UserID:            5,
		UserName:          "alice@example.com",
		APIKeyID:          6,
		APIKeyName:        "gpt",
		GroupID:           "2",
		GroupName:         "GPT subscription",
		Model:             "gpt-5.5",
		Stream:            true,
		AccountID:         3,
		AccountName:       "CIII",
		FailedAccountID:   3,
		FailedAccountName: "CIII",
		UpstreamStatus:    http.StatusBadGateway,
		SwitchCount:       1,
		MaxSwitches:       10,
	})
	require.NoError(t, err)

	require.Equal(t, "/bottest-token/sendMessage", gotPath)
	require.Equal(t, float64(12345), gotPayload["chat_id"])
	text, _ := gotPayload["text"].(string)
	require.Contains(t, text, "➡️ 502 gpt-5.5 CIII")
	require.Contains(t, text, "event: openai.upstream_failover_switching")
	require.Contains(t, text, "time: 2026-07-06 12:14:36 +0800")
	require.Contains(t, text, "route: responses")
	require.Contains(t, text, "model: gpt-5.5")
	require.Contains(t, text, "failed account: CIII (#3)")
	require.Contains(t, text, "status: 502")
	require.Contains(t, text, "switch: 1/10")
	require.Contains(t, text, "user: alice@example.com (#5)")
	require.Contains(t, text, "api key: gpt (#6)")
	require.Contains(t, text, "group: GPT subscription (#2)")
	require.NotContains(t, text, "account_id:")
	require.NotContains(t, text, "api_key_id:")
	require.NotContains(t, text, "group_id:")
	require.NotContains(t, text, "test-token")
}

func TestOpenAIAccountSwitchNotifierRateLimitsDuplicateEvents(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		sendStarted:     true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		minInterval:     time.Minute,
		lastSent:        make(map[string]time.Time),
	}
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	notifier.now = func() time.Time { return now }
	event := OpenAIAccountSwitchNotification{
		EventName:       "openai.upstream_failover_switching",
		Phase:           OpenAIAccountSwitchPhaseStarted,
		Route:           "responses",
		Model:           "gpt-5.5",
		AccountID:       1,
		FailedAccountID: 1,
		UpstreamStatus:  http.StatusBadGateway,
		SwitchCount:     1,
		MaxSwitches:     10,
	}

	require.NoError(t, notifier.Notify(context.Background(), event))
	require.NoError(t, notifier.Notify(context.Background(), event))
	require.Equal(t, int32(1), count.Load())

	now = now.Add(time.Minute + time.Second)
	require.NoError(t, notifier.Notify(context.Background(), event))
	require.Equal(t, int32(2), count.Load())
}

func TestOpenAIAccountSwitchNotifierDoesNotDedupeDifferentRequests(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		minInterval:     time.Minute,
		now:             time.Now,
		lastSent:        make(map[string]time.Time),
		inFlight:        make(map[string]struct{}),
	}
	event := OpenAIAccountSwitchNotification{
		Phase:           OpenAIAccountSwitchPhaseCompleted,
		OccurredAt:      time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
		Route:           "responses",
		Model:           "gpt-5.5",
		FailedAccountID: 1,
		TargetAccountID: 2,
		UpstreamStatus:  http.StatusBadGateway,
		FinalStatus:     http.StatusOK,
		RequestID:       "request-1",
	}

	require.NoError(t, notifier.Notify(context.Background(), event))
	event.RequestID = "request-2"
	require.NoError(t, notifier.Notify(context.Background(), event))
	require.Equal(t, int32(2), count.Load())
}

func TestOpenAIAccountSwitchNotifierFailedSendDoesNotCommitDedupe(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if count.Add(1) == 1 {
			http.Error(w, `{"ok":false,"error_code":400,"description":"bad request"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		minInterval:     time.Minute,
		now:             time.Now,
		lastSent:        make(map[string]time.Time),
		inFlight:        make(map[string]struct{}),
	}
	event := OpenAIAccountSwitchNotification{Phase: OpenAIAccountSwitchPhaseFailed, RequestID: "request-1"}

	require.Error(t, notifier.Notify(context.Background(), event))
	require.NoError(t, notifier.Notify(context.Background(), event))
	require.Equal(t, int32(2), count.Load())
}

func TestOpenAIAccountSwitchNotifierRetriesTelegramRateLimit(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if count.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"retry later","parameters":{"retry_after":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	var delays []time.Duration
	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		now:             time.Now,
		lastSent:        make(map[string]time.Time),
		sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}

	require.NoError(t, notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{Phase: OpenAIAccountSwitchPhaseFailed}))
	require.Equal(t, int32(2), count.Load())
	require.Equal(t, []time.Duration{2 * time.Second}, delays)
}

func TestOpenAIAccountSwitchNotifierRetriesTransientFailureWithExponentialBackoff(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":503,"description":"temporarily unavailable"}`))
	}))
	defer server.Close()

	var delays []time.Duration
	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		retryBaseDelay:  10 * time.Millisecond,
		sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}

	err := notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{
		Phase:       OpenAIAccountSwitchPhaseFailed,
		FinalStatus: http.StatusServiceUnavailable,
	})

	require.ErrorContains(t, err, "temporarily unavailable")
	require.Equal(t, int32(openAISwitchNotifierMaxAttempts), count.Load())
	require.Equal(t, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}, delays)
}

func TestOpenAIAccountSwitchNotifierStopsRetryWhenWaitIsInterrupted(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":502,"description":"upstream failed"}`))
	}))
	defer server.Close()

	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		retryBaseDelay:  time.Millisecond,
		sleep: func(context.Context, time.Duration) error {
			return context.Canceled
		},
	}

	err := notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{
		Phase:       OpenAIAccountSwitchPhaseFailed,
		FinalStatus: http.StatusBadGateway,
	})

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "Telegram retry interrupted")
	require.Equal(t, int32(1), count.Load())
}

func TestOpenAIAccountSwitchNotifierRejectsUnsuccessfulTwoHundredResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"chat not found"}`))
	}))
	defer server.Close()

	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		now:             time.Now,
		lastSent:        make(map[string]time.Time),
	}

	err := notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{Phase: OpenAIAccountSwitchPhaseFailed})
	require.ErrorContains(t, err, "chat not found")
}

func TestTruncateTelegramTextPreservesRuneLimit(t *testing.T) {
	text := strings.Repeat("界", telegramSendMessageMaxRunes+100)
	got := truncateTelegramText(text)
	require.Len(t, []rune(got), telegramSendMessageMaxRunes)
	require.True(t, strings.HasSuffix(got, telegramSendMessageTruncatedSuffix))
}

func TestTelegramChatIDValuePreservesNonCanonicalNumericIDs(t *testing.T) {
	tests := []struct {
		name   string
		chatID string
		want   any
	}{
		{name: "positive numeric", chatID: "12345", want: int64(12345)},
		{name: "negative channel", chatID: "-10012345", want: int64(-10012345)},
		{name: "surrounding whitespace", chatID: " 42 ", want: int64(42)},
		{name: "leading zero", chatID: "00123", want: "00123"},
		{name: "named channel", chatID: "@alerts", want: "@alerts"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, telegramChatIDValue(tc.chatID))
		})
	}
}

func TestOpenAIAccountSwitchNotifierCloseDrainsInOrder(t *testing.T) {
	var mu sync.Mutex
	var texts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		mu.Lock()
		texts = append(texts, payload.Text)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	notifier := NewOpenAIAccountSwitchNotifier(&config.Config{Gateway: config.GatewayConfig{OpenAISwitchNotify: config.GatewayOpenAISwitchNotifyConfig{
		Telegram: config.GatewayOpenAISwitchNotifyTelegramConfig{Enabled: true, BotToken: "token", ChatID: "chat", TimeoutSeconds: 5},
	}}})
	require.NotNil(t, notifier)
	notifier.apiBaseURL = server.URL
	notifier.httpClient = server.Client()
	notifier.NotifyAsync(OpenAIAccountSwitchNotification{Phase: OpenAIAccountSwitchPhaseCompleted, RequestID: "first"})
	notifier.NotifyAsync(OpenAIAccountSwitchNotification{Phase: OpenAIAccountSwitchPhaseCompleted, RequestID: "second"})
	require.NoError(t, notifier.Close(context.Background()))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, texts, 2)
	require.Contains(t, texts[0], "request_id: first")
	require.Contains(t, texts[1], "request_id: second")
}

func TestOpenAIAccountSwitchNotifierSuppressesStartedByDefault(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	notifier := NewOpenAIAccountSwitchNotifier(&config.Config{
		Gateway: config.GatewayConfig{
			OpenAISwitchNotify: config.GatewayOpenAISwitchNotifyConfig{
				Telegram: config.GatewayOpenAISwitchNotifyTelegramConfig{
					Enabled:        true,
					BotToken:       "test-token",
					ChatID:         "12345",
					TimeoutSeconds: 5,
				},
			},
		},
	})
	require.NotNil(t, notifier)
	notifier.apiBaseURL = server.URL

	require.NoError(t, notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{
		Phase:           OpenAIAccountSwitchPhaseStarted,
		FailedAccountID: 1,
		UpstreamStatus:  http.StatusBadGateway,
	}))
	require.Equal(t, int32(0), count.Load())
}

func TestOpenAIAccountSwitchNotifierDoesNotRateLimitDifferentPhases(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		sendStarted:     true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		minInterval:     time.Minute,
		lastSent:        make(map[string]time.Time),
		now:             func() time.Time { return time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC) },
	}
	base := OpenAIAccountSwitchNotification{
		EventName:       "openai.upstream_failover_switching",
		Route:           "responses",
		Model:           "gpt-5.5",
		FailedAccountID: 1,
		UpstreamStatus:  http.StatusBadGateway,
		SwitchCount:     1,
		MaxSwitches:     10,
	}

	started := base
	started.Phase = OpenAIAccountSwitchPhaseStarted
	completed := base
	completed.Phase = OpenAIAccountSwitchPhaseCompleted
	completed.TargetAccountID = 3
	completed.FinalStatus = http.StatusOK
	cancelled := base
	cancelled.Phase = OpenAIAccountSwitchPhaseCancelled
	cancelled.TargetAccountID = 3
	cancelled.FinalError = "client disconnected"

	require.NoError(t, notifier.Notify(context.Background(), started))
	require.NoError(t, notifier.Notify(context.Background(), completed))
	require.NoError(t, notifier.Notify(context.Background(), cancelled))
	require.Equal(t, int32(3), count.Load())
}

func TestOpenAIAccountSwitchNotificationTelegramTextPhasesAndFallbacks(t *testing.T) {
	when := time.Date(2026, 7, 6, 11, 38, 17, 0, time.FixedZone("CST", 8*3600))
	completed := OpenAIAccountSwitchNotification{
		Phase:             OpenAIAccountSwitchPhaseCompleted,
		OccurredAt:        when,
		Route:             "responses",
		Model:             "gpt-5.5",
		FailedAccountID:   1,
		FailedAccountName: "CIII",
		TargetAccountID:   3,
		TargetAccountName: "AiNX",
		UpstreamStatus:    http.StatusBadGateway,
		FinalStatus:       http.StatusOK,
		LatencyMs:         194772,
		UserID:            3,
		APIKeyID:          2,
		APIKeyName:        "gpt",
		GroupID:           "2",
		GroupName:         "GPT subscription",
	}
	text := completed.telegramText()
	require.Contains(t, text, "✅ CIII -> AiNX")
	require.Contains(t, text, "time: 2026-07-06 11:38:17 +0800")
	require.Contains(t, text, "from: CIII (#1)")
	require.Contains(t, text, "to: AiNX (#3)")
	require.Contains(t, text, "error status: 502")
	require.NotContains(t, text, "final status: 200")
	require.Contains(t, text, "latency: 194.8s")
	require.Contains(t, text, "user: #3")
	require.Contains(t, text, "api key: gpt (#2)")
	require.Contains(t, text, "group: GPT subscription (#2)")

	failed := OpenAIAccountSwitchNotification{
		Phase:             OpenAIAccountSwitchPhaseFailed,
		OccurredAt:        when,
		FailedAccountID:   1,
		TargetAccountID:   2,
		TargetAccountName: "fallback",
		FinalStatus:       http.StatusBadGateway,
		FinalError:        "context canceled",
	}
	text = failed.telegramText()
	require.Contains(t, text, "❌ 502 #1")
	require.Contains(t, text, "from: #1")
	require.Contains(t, text, "attempted: fallback (#2)")
	require.Contains(t, text, "final status: 502")
	require.Contains(t, text, "reason: context canceled")

	streamFailed := OpenAIAccountSwitchNotification{
		Phase:             OpenAIAccountSwitchPhaseFailed,
		OccurredAt:        when,
		FailedAccountID:   10,
		FailedAccountName: "ixo-plus",
		Model:             "gpt-5.5",
		FinalStatus:       http.StatusBadGateway,
		FinalError:        "upstream response failed: Upstream request failed",
		ClientStatus:      http.StatusOK,
		StreamStarted:     true,
		UpstreamWritten:   true,
	}
	text = streamFailed.telegramText()
	require.Contains(t, text, "⚠️ OpenAI stream failed after start")
	require.Contains(t, text, "from: ixo-plus (#10)")
	require.Contains(t, text, "final status: 502")
	require.Contains(t, text, "client status: 200")
	require.Contains(t, text, "stream started: true")
	require.Contains(t, text, "fallback response written: false")
	require.Contains(t, text, "upstream response already written: true")
	require.Contains(t, text, "retry possible: false")
	require.Contains(t, text, "reason: upstream response failed: Upstream request failed")

	cancelled := OpenAIAccountSwitchNotification{
		Phase:             OpenAIAccountSwitchPhaseCancelled,
		OccurredAt:        when,
		FailedAccountID:   1,
		FailedAccountName: "CIII",
		TargetAccountID:   3,
		TargetAccountName: "AiNX",
		FinalError:        "client disconnected",
	}
	text = cancelled.telegramText()
	require.Contains(t, text, "⚠️ CIII -> AiNX")
	require.Contains(t, text, "from: CIII (#1)")
	require.Contains(t, text, "to: AiNX (#3)")
	require.Contains(t, text, "reason: client disconnected")

	failback := OpenAIAccountSwitchNotification{
		EventName:         "openai.account_failback_to_highest_priority",
		Phase:             OpenAIAccountSwitchPhaseFailback,
		OccurredAt:        when,
		Route:             "responses",
		Model:             "gpt-5.5",
		FailedAccountID:   3,
		FailedAccountName: "Backup",
		FailedPriority:    20,
		TargetAccountID:   1,
		TargetAccountName: "Main",
		TargetPriority:    1,
		FinalError:        "higher_priority_available",
		UserID:            3,
		UserName:          "alice",
		APIKeyID:          2,
		APIKeyName:        "codex",
		GroupID:           "2",
		GroupName:         "GPT subscription",
	}
	text = failback.telegramText()
	require.Contains(t, text, "❤️ Backup -> Main")
	require.Contains(t, text, "event: openai.account_failback_to_highest_priority")
	require.Contains(t, text, "from: Backup (#3) priority=20")
	require.Contains(t, text, "to: Main (#1) priority=1")
	require.Contains(t, text, "reason: higher_priority_available")
	require.Contains(t, text, "user: alice (#3)")
	require.Contains(t, text, "api key: codex (#2)")
	require.Contains(t, text, "group: GPT subscription (#2)")
}

func TestNewOpenAIAccountSwitchNotifierDisabledOrIncomplete(t *testing.T) {
	require.Nil(t, NewOpenAIAccountSwitchNotifier(nil))
	require.Nil(t, NewOpenAIAccountSwitchNotifier(&config.Config{}))
	require.Nil(t, NewOpenAIAccountSwitchNotifier(&config.Config{
		Gateway: config.GatewayConfig{
			OpenAISwitchNotify: config.GatewayOpenAISwitchNotifyConfig{
				Telegram: config.GatewayOpenAISwitchNotifyTelegramConfig{Enabled: true},
			},
		},
	}))
}

func TestOpenAIAccountSwitchNotifierTelegramError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed https://api.telegram.org/bottoken/sendMessage "+strings.Repeat("x", 5000), http.StatusTooManyRequests)
	}))
	defer server.Close()

	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        "token",
		chatID:          "chat",
		apiBaseURL:      server.URL,
		httpClient:      server.Client(),
		timeout:         5 * time.Second,
		now:             time.Now,
		lastSent:        make(map[string]time.Time),
	}
	err := notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{
		Phase:           OpenAIAccountSwitchPhaseFailed,
		FailedAccountID: 1,
		FinalStatus:     http.StatusBadGateway,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "telegram returned status 429")
	require.NotContains(t, err.Error(), "bottoken")
	require.Contains(t, err.Error(), "bot<redacted>/sendMessage")
}

func TestOpenAIAccountSwitchNotifierRedactsTokenFromTransportError(t *testing.T) {
	notifier := &OpenAIAccountSwitchNotifier{
		telegramEnabled: true,
		botToken:        "secret-token",
		chatID:          "chat",
		apiBaseURL:      defaultOpenAISwitchNotifierTelegramBaseURL,
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("Post https://api.telegram.org/botsecret-token/sendMessage: dial failed")
		})},
		timeout:  5 * time.Second,
		now:      time.Now,
		lastSent: make(map[string]time.Time),
	}

	err := notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{
		Phase:           OpenAIAccountSwitchPhaseFailed,
		FailedAccountID: 1,
		FinalStatus:     http.StatusBadGateway,
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-token")
	require.Contains(t, err.Error(), "<redacted>")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

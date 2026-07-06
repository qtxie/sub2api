package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	require.Contains(t, text, "sub2api OpenAI failover started")
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

func TestOpenAIAccountSwitchNotifierDoesNotRateLimitDifferentPhases(t *testing.T) {
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

	require.NoError(t, notifier.Notify(context.Background(), started))
	require.NoError(t, notifier.Notify(context.Background(), completed))
	require.Equal(t, int32(2), count.Load())
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
		FinalStatus:       http.StatusOK,
		LatencyMs:         194772,
		UserID:            3,
		APIKeyID:          2,
		APIKeyName:        "gpt",
		GroupID:           "2",
		GroupName:         "GPT subscription",
	}
	text := completed.telegramText()
	require.Contains(t, text, "sub2api OpenAI failover completed")
	require.Contains(t, text, "time: 2026-07-06 11:38:17 +0800")
	require.Contains(t, text, "from: CIII (#1)")
	require.Contains(t, text, "to: AiNX (#3)")
	require.Contains(t, text, "final status: 200")
	require.Contains(t, text, "latency: 194.8s")
	require.Contains(t, text, "user: #3")
	require.Contains(t, text, "api key: gpt (#2)")
	require.Contains(t, text, "group: GPT subscription (#2)")

	failed := OpenAIAccountSwitchNotification{
		Phase:           OpenAIAccountSwitchPhaseFailed,
		OccurredAt:      when,
		FailedAccountID: 1,
		FinalStatus:     http.StatusBadGateway,
		FinalError:      "context canceled",
	}
	text = failed.telegramText()
	require.Contains(t, text, "sub2api OpenAI failover failed")
	require.Contains(t, text, "from: #1")
	require.Contains(t, text, "final status: 502")
	require.Contains(t, text, "reason: context canceled")
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
	err := notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{AccountID: 1, UpstreamStatus: http.StatusBadGateway})
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

	err := notifier.Notify(context.Background(), OpenAIAccountSwitchNotification{AccountID: 1, UpstreamStatus: http.StatusBadGateway})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-token")
	require.Contains(t, err.Error(), "<redacted>")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

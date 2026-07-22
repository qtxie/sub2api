package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type telegramOutboxStub struct {
	mu      sync.Mutex
	events  []GatewayNotificationEvent
	keys    []string
	buckets []int64
}

type gatewayNotificationPublisherStub struct {
	mu     sync.Mutex
	events []GatewayNotificationEvent
}

func (s *gatewayNotificationPublisherStub) PublishGatewayNotification(event GatewayNotificationEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *telegramOutboxStub) Enqueue(_ context.Context, event GatewayNotificationEvent, key string, bucket int64, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	s.keys = append(s.keys, key)
	s.buckets = append(s.buckets, bucket)
	return nil
}

func (s *telegramOutboxStub) Claim(context.Context, string, int, time.Duration) ([]TelegramNotificationOutboxEvent, error) {
	return nil, nil
}

func (s *telegramOutboxStub) MarkDelivered(context.Context, int64, string) error { return nil }

func (s *telegramOutboxStub) RetryClaimed(context.Context, int64, string, time.Time, string) error {
	return nil
}

func (s *telegramOutboxStub) DeleteDeliveredBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func newTelegramNotificationTestService(t *testing.T, outbox TelegramNotificationOutboxRepository) *TelegramNotificationService {
	t.Helper()
	return NewTelegramNotificationService(outbox, &config.Config{
		Notifications: config.NotificationsConfig{Telegram: config.TelegramNotificationConfig{
			Enabled:               true,
			BotToken:              "123456:bot-token",
			ChatID:                "-1001234567890,@ops_channel",
			ThreadID:              42,
			NotifyErrors:          true,
			NotifyTimeouts:        true,
			NotifyAccountSwitches: true,
			DedupeWindowSeconds:   300,
			QueueSize:             32,
			RequestTimeoutSeconds: 10,
		}},
	})
}

func TestTelegramNotificationPersistsConfiguredGatewayEventWithDedupe(t *testing.T) {
	outbox := &telegramOutboxStub{}
	svc := newTelegramNotificationTestService(t, outbox)

	event := GatewayNotificationEvent{
		Type:       GatewayNotificationEventSwitch,
		Platform:   "openai",
		AccountID:  42,
		Model:      "gpt-5",
		StatusCode: http.StatusBadGateway,
		Reason:     "inference failure",
	}
	if !normalizeGatewayNotificationEvent(&event) {
		t.Fatal("normalizeGatewayNotificationEvent() = false")
	}
	svc.persistEvent(event)

	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.events) != 1 {
		t.Fatalf("outbox events = %d, want 1", len(outbox.events))
	}
	if len(outbox.keys) != 1 || len(outbox.keys[0]) != 64 {
		t.Fatalf("dedupe key = %#v, want SHA-256 hex key", outbox.keys)
	}
	if outbox.events[0].Type != GatewayNotificationEventSwitch || outbox.events[0].OccurredAt.IsZero() {
		t.Fatalf("stored event = %#v", outbox.events[0])
	}
}

func TestTelegramNotificationDedupeSeparatesUsers(t *testing.T) {
	event := GatewayNotificationEvent{
		Type:        GatewayNotificationEventError,
		Platform:    PlatformOpenAI,
		UserID:      1,
		UserName:    "admin@sub2api.local",
		AccountID:   13,
		AccountName: "ciii_admin",
		Model:       "grok-4.5",
		StatusCode:  http.StatusServiceUnavailable,
		Reason:      "upstream request failed",
	}
	otherUser := event
	otherUser.UserID = 2
	otherUser.UserName = "other@sub2api.local"

	if telegramNotificationDedupeKey(event) == telegramNotificationDedupeKey(otherUser) {
		t.Fatal("dedupe key must include the affected user")
	}
}

func TestTelegramNotificationFiltersDisabledEventFromConfig(t *testing.T) {
	configValue := &config.Config{Notifications: config.NotificationsConfig{Telegram: config.TelegramNotificationConfig{
		Enabled:               true,
		BotToken:              "123456:bot-token",
		ChatID:                "-1001234567890",
		NotifyErrors:          true,
		NotifyTimeouts:        false,
		NotifyAccountSwitches: true,
	}}}
	outbox := &telegramOutboxStub{}
	svc := NewTelegramNotificationService(outbox, configValue)
	svc.persistEvent(GatewayNotificationEvent{Type: GatewayNotificationEventTimeout})

	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.events) != 0 {
		t.Fatalf("outbox events = %d, want 0", len(outbox.events))
	}
}

func TestTelegramNotificationSendsConfiguredThread(t *testing.T) {
	svc := newTelegramNotificationTestService(t, &telegramOutboxStub{})
	var payload struct {
		ChatID          string `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id"`
		Text            string `json:"text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123456:bot-token/sendMessage" {
			t.Errorf("unexpected Telegram API path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "bot-token") {
			t.Errorf("Telegram payload leaked bot token: %s", body)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode Telegram payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	svc.telegramAPIBaseURL = server.URL
	svc.httpClient = server.Client()

	if _, err := svc.sendMessage(context.Background(), "123456:bot-token", "-1001234567890", 42, "hello"); err != nil {
		t.Fatalf("sendMessage() error = %v", err)
	}
	if payload.ChatID != "-1001234567890" || payload.MessageThreadID != 42 || payload.Text != "hello" {
		t.Fatalf("Telegram payload = %#v", payload)
	}
}

func TestTelegramNotificationDoesNotQueueWithoutCredentials(t *testing.T) {
	svc := NewTelegramNotificationService(&telegramOutboxStub{}, &config.Config{
		Notifications: config.NotificationsConfig{Telegram: config.TelegramNotificationConfig{Enabled: true}},
	})
	svc.persistEvent(GatewayNotificationEvent{Type: GatewayNotificationEventError})
	if svc.persisted.Load() != 0 || svc.suppressed.Load() != 1 {
		t.Fatalf("persisted=%d suppressed=%d, want 0 and 1", svc.persisted.Load(), svc.suppressed.Load())
	}
}

func TestOpenAIGatewayPublishesErrorTimeoutAndSwitchEvents(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	publisher := &gatewayNotificationPublisherStub{}
	gateway := &OpenAIGatewayService{}
	gateway.SetGatewayNotificationPublisher(publisher)
	notificationCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(1))
	notificationCtx = context.WithValue(notificationCtx, ctxkey.UserDisplayName, "admin@sub2api.local")

	gateway.ReportOpenAIAccountScheduleFailure(
		&Account{ID: 7, Name: "account A", Platform: PlatformOpenAI},
		&User{ID: 1, Email: "admin@sub2api.local"},
		"gpt-5",
		http.StatusBadGateway,
	)
	gateway.ReportOpenAIUpstreamTimeout(
		notificationCtx,
		&Account{ID: 7, Name: "account A", Platform: PlatformOpenAI},
		"gpt-5",
		http.StatusGatewayTimeout,
		"first_output",
		"first output deadline exceeded",
		1500*time.Millisecond,
	)
	gateway.ReportOpenAIAccountSwitchTransition(
		&Account{ID: 7, Name: "account A", Priority: 0},
		&Account{ID: 8, Name: "account B", Priority: 5},
		&User{ID: 1, Email: "admin@sub2api.local"},
		"gpt-5",
		http.StatusBadGateway,
		"inference",
	)

	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if len(publisher.events) != 3 {
		t.Fatalf("published events = %d, want 3", len(publisher.events))
	}
	wantTypes := []GatewayNotificationEventType{
		GatewayNotificationEventError,
		GatewayNotificationEventTimeout,
		GatewayNotificationEventSwitch,
	}
	for i, want := range wantTypes {
		if publisher.events[i].Type != want {
			t.Fatalf("event %d type = %q, want %q", i, publisher.events[i].Type, want)
		}
	}
	if publisher.events[2].StatusCode != http.StatusBadGateway || publisher.events[2].FromAccountID != 7 || publisher.events[2].ToAccountID != 8 || publisher.events[2].UserID != 1 || publisher.events[2].UserName != "admin@sub2api.local" {
		t.Fatalf("switch event = %#v", publisher.events[2])
	}
	if publisher.events[0].StatusCode != http.StatusBadGateway || publisher.events[0].AccountName != "account A" || publisher.events[0].UserName != "admin@sub2api.local" || publisher.events[0].UserID != 1 {
		t.Fatalf("error event = %#v", publisher.events[0])
	}
	if publisher.events[1].ElapsedMs != 1500 || publisher.events[1].UserID != 1 || publisher.events[1].UserName != "admin@sub2api.local" {
		t.Fatalf("timeout elapsed_ms = %d, want 1500", publisher.events[1].ElapsedMs)
	}
}

func TestFormatTelegramGatewayNotificationUsesRequestedFormats(t *testing.T) {
	tests := []struct {
		name  string
		event TelegramNotificationOutboxEvent
		want  string
	}{
		{
			name: "error",
			event: TelegramNotificationOutboxEvent{
				Event: GatewayNotificationEvent{
					Type:        GatewayNotificationEventError,
					Platform:    "openai",
					UserID:      1,
					UserName:    "admin@sub2api.local",
					AccountID:   7,
					AccountName: "account A",
					Model:       "gpt-5",
					StatusCode:  http.StatusBadGateway,
					Stage:       "inference",
					Reason:      "upstream connection reset",
				},
				OccurrenceCount: 3,
				LastOccurredAt:  time.Date(2026, time.July, 22, 1, 3, 55, 0, time.UTC),
			},
			want: "❌ ERROR 502\nPlatform: openai\nUser: admin@sub2api.local (1)\nAccount: account A (7)\nModel: gpt-5\nStage: inference\nReason: upstream connection reset\nOccurrences: 3\nLast seen: 2026-07-22T01:03:55Z",
		},
		{
			name: "error without status",
			event: TelegramNotificationOutboxEvent{
				Event: GatewayNotificationEvent{
					Type:       GatewayNotificationEventError,
					Platform:   "openai",
					AccountID:  13,
					Model:      "grok-4.5",
					Reason:     "upstream request failed",
					OccurredAt: time.Date(2026, time.July, 22, 1, 3, 55, 0, time.UTC),
				},
			},
			want: "❌ ERROR\nPlatform: openai\nAccount: 13\nModel: grok-4.5\nReason: upstream request failed\nLast seen: 2026-07-22T01:03:55Z",
		},
		{
			name: "timeout",
			event: TelegramNotificationOutboxEvent{
				Event: GatewayNotificationEvent{
					Type:        GatewayNotificationEventTimeout,
					Platform:    "openai",
					UserID:      1,
					UserName:    "admin@sub2api.local",
					AccountID:   7,
					AccountName: "account A",
					Model:       "gpt-5",
					StatusCode:  http.StatusGatewayTimeout,
					Stage:       "first_output",
					Reason:      "first output deadline exceeded",
					ElapsedMs:   1500,
					OccurredAt:  time.Date(2026, time.July, 22, 1, 3, 55, 0, time.UTC),
				},
			},
			want: "⚠️ TIMEOUT 1.5s\nPlatform: openai\nUser: admin@sub2api.local (1)\nAccount: account A (7)\nModel: gpt-5\nStatus: 504\nStage: first_output\nReason: first output deadline exceeded\nLast seen: 2026-07-22T01:03:55Z",
		},
		{
			name: "switch to backup",
			event: TelegramNotificationOutboxEvent{
				Event: GatewayNotificationEvent{
					Type:            GatewayNotificationEventSwitch,
					Platform:        "openai",
					UserID:          1,
					UserName:        "admin@sub2api.local",
					FromAccountID:   7,
					FromAccountName: "A",
					ToAccountID:     8,
					ToAccountName:   "B",
					Model:           "gpt-5",
					StatusCode:      http.StatusBadGateway,
					Stage:           "inference",
					Reason:          "upstream unavailable",
					FromPriority:    0,
					ToPriority:      5,
					OccurredAt:      time.Date(2026, time.July, 22, 1, 3, 55, 0, time.UTC),
				},
				OccurrenceCount: 2,
			},
			want: "✅ account A -> account B\nFrom: A (7); priority 0\nTo: B (8); priority 5\nPlatform: openai\nUser: admin@sub2api.local (1)\nModel: gpt-5\nStatus: 502\nStage: inference\nReason: upstream unavailable\nOccurrences: 2\nLast seen: 2026-07-22T01:03:55Z",
		},
		{
			name: "switch to primary",
			event: TelegramNotificationOutboxEvent{Event: GatewayNotificationEvent{
				Type: GatewayNotificationEventSwitch, FromAccountName: "B", ToAccountName: "A", FromPriority: 5, ToPriority: 0,
			}},
			want: "❤️ account B -> account A\nFrom: B; priority 5\nTo: A; priority 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTelegramGatewayNotificationInLocation(tt.event, time.UTC); got != tt.want {
				t.Fatalf("formatTelegramGatewayNotification() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatTelegramGatewayNotificationUsesLocalTime(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Asia/Shanghai: %v", err)
	}

	message := formatTelegramGatewayNotificationInLocation(TelegramNotificationOutboxEvent{
		Event: GatewayNotificationEvent{
			Type:       GatewayNotificationEventError,
			OccurredAt: time.Date(2026, time.July, 22, 1, 3, 55, 0, time.UTC),
		},
	}, shanghai)

	if !strings.Contains(message, "Last seen: 2026-07-22T09:03:55+08:00") {
		t.Fatalf("message = %q, want Asia/Shanghai timestamp", message)
	}
}

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

	gateway.ReportOpenAIAccountScheduleResult(7, "gpt-5", false, nil)
	gateway.ReportOpenAIUpstreamTimeout(7, "gpt-5", http.StatusGatewayTimeout, "first output deadline exceeded")
	gateway.ReportOpenAIAccountSwitchEvent(7, "gpt-5", http.StatusBadGateway, "inference")

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
	if publisher.events[2].StatusCode != http.StatusBadGateway || publisher.events[2].AccountID != 7 {
		t.Fatalf("switch event = %#v", publisher.events[2])
	}
}

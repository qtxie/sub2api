package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type preOutputFailoverAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r preOutputFailoverAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

func (r preOutputFailoverAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	var accounts []service.Account
	for _, account := range r.accounts {
		if account.Platform == platform {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

type preOutputFailoverHTTPUpstream struct {
	service.HTTPUpstream
	mu                sync.Mutex
	accountIDs        []int64
	timeoutAccountIDs map[int64]struct{}
}

type recordingOpenAIAccountSwitchNotifier struct {
	mu     sync.Mutex
	events []service.OpenAIAccountSwitchNotification
}

func (n *recordingOpenAIAccountSwitchNotifier) NotifyAsync(event service.OpenAIAccountSwitchNotification) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event)
}

func (n *recordingOpenAIAccountSwitchNotifier) Close(context.Context) error {
	return nil
}

func (n *recordingOpenAIAccountSwitchNotifier) notifications() []service.OpenAIAccountSwitchNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]service.OpenAIAccountSwitchNotification(nil), n.events...)
}

func (u *preOutputFailoverHTTPUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()
	shouldTimeout := accountID == 1
	if u.timeoutAccountIDs != nil {
		_, shouldTimeout = u.timeoutAccountIDs[accountID]
	}
	if shouldTimeout {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"req_second"}},
		Body: io.NopCloser(bytes.NewBufferString(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_second\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_second\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
		)),
	}, nil
}

func (u *preOutputFailoverHTTPUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func TestPreOutputFinalErrorAfterHeartbeatUsesResponsesSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	stop := service.StartOpenAIPreOutput(c, service.OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        5 * time.Second,
		HeartbeatInterval:  10 * time.Millisecond,
	})
	defer stop()

	deadline := time.Now().Add(250 * time.Millisecond)
	for !service.OpenAIPreOutputTransportStarted(c) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !service.OpenAIPreOutputTransportStarted(c) {
		t.Fatal("heartbeat did not commit transport")
	}

	h := &OpenAIGatewayHandler{}
	h.errorResponse(c, http.StatusGatewayTimeout, "first_output_timeout", "upstream did not produce output")
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("committed heartbeat status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(body, "event: response.failed") || !strings.Contains(body, "upstream did not produce output") {
		t.Fatalf("expected structured Responses SSE failure, got %q", body)
	}
}

func TestPreOutputFinalErrorBeforeHeartbeatKeepsHTTPStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	stop := service.StartOpenAIPreOutput(c, service.OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        5 * time.Second,
		HeartbeatInterval:  time.Hour,
	})
	defer stop()

	h := &OpenAIGatewayHandler{}
	h.errorResponse(c, http.StatusGatewayTimeout, "first_output_timeout", "upstream did not produce output")
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("content type = %q, want JSON", contentType)
	}
}

func TestAcquireResponsesAccountSlotKeepsReleaseThroughBillingDrain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	stop := service.StartOpenAIPreOutput(c, service.OpenAIPreOutputSettings{
		FirstOutputTimeout: time.Second,
		TotalBudget:        5 * time.Second,
	})
	defer stop()

	var released atomic.Int32
	h := &OpenAIGatewayHandler{}
	release, ok := h.acquireResponsesAccountSlot(c, nil, "", &service.AccountSelectionResult{
		Account:     &service.Account{ID: 1, Name: "acc"},
		Acquired:    true,
		ReleaseFunc: func() { released.Add(1) },
	}, true, new(bool), nil)
	if !ok || release == nil {
		t.Fatal("expected acquired account slot")
	}
	cancel()
	time.Sleep(25 * time.Millisecond)
	if released.Load() != 0 {
		t.Fatal("account slot should remain held after client cancellation while billing drain is active")
	}
	release()
	if released.Load() != 1 {
		t.Fatal("account slot release should still execute exactly once")
	}
}

func TestOpenAIFailoverFailedEventNameMapsAllRoutes(t *testing.T) {
	if got := openAIFailoverFailedEventName("openai.upstream_failover_switching"); got != "openai.upstream_failover_failed" {
		t.Fatalf("responses event name = %q", got)
	}
	if got := openAIFailoverFailedEventName("openai_messages.upstream_failover_switching"); got != "openai_messages.upstream_failover_failed" {
		t.Fatalf("messages event name = %q", got)
	}
}

func TestFirstOutputTimeoutIsAccountedBeforeFailoverBudgetGate(t *testing.T) {
	var calls []string
	canFailover := accountOpenAIFailureBeforeFailoverDecision(
		&service.UpstreamFailoverError{FirstOutputTimeout: true, AttemptLatencyMs: 60000},
		func(attemptMs *int) {
			calls = append(calls, "timeout")
			if attemptMs == nil || *attemptMs != 60000 {
				t.Fatalf("attempt latency = %v, want 60000", attemptMs)
			}
		},
		func() { calls = append(calls, "generic") },
		func() bool {
			calls = append(calls, "gate")
			return false
		},
	)
	if canFailover {
		t.Fatal("budget gate should deny failover")
	}
	if got := strings.Join(calls, ","); got != "timeout,gate" {
		t.Fatalf("call order = %q, want timeout,gate", got)
	}
}

func TestGenericFailureIsNotAccountedWhenFailoverGateRejectsIt(t *testing.T) {
	var genericReports int
	canFailover := accountOpenAIFailureBeforeFailoverDecision(
		&service.UpstreamFailoverError{StatusCode: http.StatusBadGateway},
		nil,
		func() { genericReports++ },
		func() bool { return false },
	)
	if canFailover || genericReports != 0 {
		t.Fatalf("canFailover=%v genericReports=%d, want false/0", canFailover, genericReports)
	}
}

func TestPreOutputBudgetExhaustionDoesNotPenalizeAccount(t *testing.T) {
	var timeoutReports, genericReports int
	canFailover := accountOpenAIFailureBeforeFailoverDecision(
		&service.UpstreamFailoverError{PreOutputBudgetExhausted: true, AttemptLatencyMs: 30000},
		func(*int) { timeoutReports++ },
		func() { genericReports++ },
		func() bool { return false },
	)
	if canFailover || timeoutReports != 0 || genericReports != 0 {
		t.Fatalf("canFailover=%v timeoutReports=%d genericReports=%d, want false/0/0", canFailover, timeoutReports, genericReports)
	}
}

func TestOpenAIFailoverTerminalNotificationDetailsPreservesTypedTimeout(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	status, reason := h.openAIFailoverTerminalNotificationDetails(&service.UpstreamFailoverError{
		StatusCode:         http.StatusGatewayTimeout,
		FirstOutputTimeout: true,
		AttemptLatencyMs:   60000,
	}, "no available accounts")

	require.Equal(t, http.StatusGatewayTimeout, status)
	require.Contains(t, reason, "first output timeout")
	require.NotContains(t, reason, "no available accounts")
}

func TestAdvanceOpenAIAccountSwitchDoesNotFailSilentTimeoutTransition(t *testing.T) {
	notifier := &recordingOpenAIAccountSwitchNotifier{}
	h := &OpenAIGatewayHandler{accountSwitchNotifier: notifier}
	previous := newOpenAIAccountSwitchAttempt(
		"openai.upstream_failover_switching",
		"responses",
		"gpt-5",
		true,
		&service.Account{ID: 1, Name: "slow-one"},
		http.StatusGatewayTimeout,
		1,
		0,
	)

	h.advanceOpenAIAccountSwitch(
		nil,
		previous,
		"openai.upstream_failover_switching",
		"responses",
		nil,
		0,
		"gpt-5",
		true,
		&service.Account{ID: 2, Name: "failed-target"},
		http.StatusBadGateway,
		1,
		3,
	)

	events := notifier.notifications()
	require.Len(t, events, 1)
	require.Equal(t, service.OpenAIAccountSwitchPhaseStarted, events[0].Phase)
	require.Equal(t, int64(2), events[0].FailedAccountID)
}

func TestShouldStartOpenAIPreOutputMatchesInitialRolloutScope(t *testing.T) {
	subscriptionKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, SubscriptionType: service.SubscriptionTypeSubscription}}
	standardKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, SubscriptionType: service.SubscriptionTypeStandard}}
	grokKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformGrok, SubscriptionType: service.SubscriptionTypeSubscription}}

	tests := []struct {
		name                         string
		stream, compact, imageIntent bool
		apiKey                       *service.APIKey
		want                         bool
	}{
		{name: "ordinary subscription responses", stream: true, apiKey: subscriptionKey, want: true},
		{name: "standard group", stream: true, apiKey: standardKey},
		{name: "sync responses", apiKey: subscriptionKey},
		{name: "compact", stream: true, compact: true, apiKey: subscriptionKey},
		{name: "image", stream: true, imageIntent: true, apiKey: subscriptionKey},
		{name: "grok", stream: true, apiKey: grokKey},
		{name: "missing api key", stream: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldStartOpenAIPreOutput(tt.stream, tt.compact, tt.imageIntent, tt.apiKey); got != tt.want {
				t.Fatalf("shouldStartOpenAIPreOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResponsesFirstOutputTimeoutFailsOverToSecondAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(44)
	accounts := []service.Account{
		{ID: 1, Name: "slow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"access_token": "slow-token"}},
		{ID: 2, Name: "healthy", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"access_token": "healthy-token"}},
	}
	repo := preOutputFailoverAccountRepo{accounts: accounts}
	upstream := &preOutputFailoverHTTPUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	cfg.Gateway.OpenAITotalPreOutputBudgetSeconds = 4
	cfg.Gateway.OpenAIPreOutputDisconnectDrainSeconds = 1
	cfg.Gateway.OpenAIPostOutputBillingDrainSeconds = 1

	concurrencyService := service.NewConcurrencyService(nil)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gatewayService := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrencyService,
		nil, nil, billingCache, upstream, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)

	body := []byte(`{"model":"gpt-5","stream":true,"input":"hello"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      9,
		GroupID: &groupID,
		Group: &service.Group{
			ID:               groupID,
			Platform:         service.PlatformOpenAI,
			SubscriptionType: service.SubscriptionTypeSubscription,
		},
		User: &service.User{ID: 7},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})

	startedAt := time.Now()
	h.Responses(c)

	require.Equal(t, []int64{1, 2}, upstream.calls())
	require.Less(t, time.Since(startedAt), 3*time.Second)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.output_text.delta")
	require.Contains(t, rec.Body.String(), "hello")
	require.NotContains(t, rec.Body.String(), `"account":"slow"`)
	require.False(t, strings.HasPrefix(strings.TrimSpace(rec.Body.String()), `{"error":`))
}

func TestResponsesFirstOutputTimeoutUsesSlowAccountFailoverUntilSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(45)
	accounts := []service.Account{
		{ID: 1, Name: "slow-one", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"access_token": "slow-one-token"}},
		{ID: 2, Name: "slow-two", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"access_token": "slow-two-token"}},
		{ID: 3, Name: "healthy", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 2, Credentials: map[string]any{"access_token": "healthy-token"}},
	}
	repo := preOutputFailoverAccountRepo{accounts: accounts}
	upstream := &preOutputFailoverHTTPUpstream{timeoutAccountIDs: map[int64]struct{}{1: {}, 2: {}}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	cfg.Gateway.OpenAITotalPreOutputBudgetSeconds = 3
	cfg.Gateway.OpenAIPreOutputDisconnectDrainSeconds = 1
	cfg.Gateway.OpenAIPostOutputBillingDrainSeconds = 1
	cfg.Gateway.MaxAccountSwitches = 1

	concurrencyService := service.NewConcurrencyService(nil)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gatewayService := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrencyService,
		nil, nil, billingCache, upstream, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)
	notifier := &recordingOpenAIAccountSwitchNotifier{}
	h.accountSwitchNotifier = notifier

	body := []byte(`{"model":"gpt-5","stream":true,"input":"hello"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      10,
		GroupID: &groupID,
		Group: &service.Group{
			ID:               groupID,
			Platform:         service.PlatformOpenAI,
			SubscriptionType: service.SubscriptionTypeSubscription,
		},
		User: &service.User{ID: 8},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 8})

	startedAt := time.Now()
	h.Responses(c)

	require.Equal(t, []int64{1, 2, 3}, upstream.calls(), "status=%d body=%s", rec.Code, rec.Body.String())
	require.Less(t, time.Since(startedAt), 4*time.Second)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.output_text.delta")
	require.Contains(t, rec.Body.String(), "hello")
	require.NotContains(t, rec.Body.String(), `{"account":"slow-one"`)
	require.NotContains(t, rec.Body.String(), `{"account":"slow-two"`)
	require.False(t, strings.HasPrefix(strings.TrimSpace(rec.Body.String()), `{"error":`))

	events := notifier.notifications()
	require.Len(t, events, 3)
	require.Equal(t, []string{
		"openai.account_first_output_timeout",
		"openai.account_first_output_timeout",
		"openai.upstream_failover_completed",
	}, []string{events[0].EventName, events[1].EventName, events[2].EventName})
	require.Equal(t, []string{
		service.OpenAIAccountSwitchPhaseFailed,
		service.OpenAIAccountSwitchPhaseFailed,
		service.OpenAIAccountSwitchPhaseCompleted,
	}, []string{events[0].Phase, events[1].Phase, events[2].Phase})
}

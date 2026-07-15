package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIFirstOutputBlockingBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *openAIFirstOutputBlockingBody) Read(_ []byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *openAIFirstOutputBlockingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

type openAIFirstOutputFailoverUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
}

func (u *openAIFirstOutputFailoverUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	call := len(u.accountIDs)
	u.mu.Unlock()

	body := io.ReadCloser(&openAIFirstOutputBlockingBody{closed: make(chan struct{})})
	if call > 1 {
		body = io.NopCloser(bytes.NewBufferString("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_failover\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}, nil
}

func (u *openAIFirstOutputFailoverUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func TestOpenAIForwardMayFailoverOnlyAfterNonSemanticWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)

	_, err := fmt.Fprint(c.Writer, ":\n\n")
	require.NoError(t, err)
	c.Writer.Flush()

	require.True(t, openAIForwardMayFailover(c, before, &service.UpstreamFailoverError{
		SafeToFailoverAfterWrite: true,
	}))
	require.False(t, openAIForwardMayFailover(c, before, &service.UpstreamFailoverError{}))
}

func TestOpenAIFirstOutputFailoverStopsAfterOneAccountSwitch(t *testing.T) {
	failoverErr := &service.UpstreamFailoverError{SafeToFailoverAfterWrite: true}
	count := 0

	require.False(t, openAIFirstOutputFailoverExhausted(failoverErr, &count))
	require.Equal(t, 1, count)
	require.True(t, openAIFirstOutputFailoverExhausted(failoverErr, &count))
	require.Equal(t, 1, count)
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

func TestOpenAIFailoverFailedEventNameMapsAllRoutes(t *testing.T) {
	require.Equal(t, "openai.upstream_failover_failed", openAIFailoverFailedEventName("openai.upstream_failover_switching"))
	require.Equal(t, "openai_messages.upstream_failover_failed", openAIFailoverFailedEventName("openai_messages.upstream_failover_switching"))
}

func TestOpenAIRequestAllowsFailoverReplayStopsCanceledClient(t *testing.T) {
	require.False(t, openAIRequestAllowsFailoverReplay(nil))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	requestCtx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestCtx)

	require.True(t, openAIRequestAllowsFailoverReplay(c))
	cancel()
	require.False(t, openAIRequestAllowsFailoverReplay(c))
}

func TestOpenAIResponsesFirstOutputTimeoutSwitchesAccountWithoutSameAccountReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4204)
	accounts := []service.Account{
		{
			ID: 9920, Name: "slow-oauth", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{"access_token": "token-1", "chatgpt_account_id": "account-1"},
		},
		{
			ID: 9921, Name: "fast-oauth", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 2,
			Credentials: map[string]any{"access_token": "token-2", "chatgpt_account_id": "account-2"},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	cfg.Gateway.MaxAccountSwitches = 3

	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	upstream := &openAIFirstOutputFailoverUpstream{}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCacheSvc, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(
		gatewaySvc,
		service.NewConcurrencyService(nil),
		billingCacheSvc,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.2","input":"hello","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 1804, GroupID: &groupID,
		User:  &service.User{ID: 1704, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1704, Concurrency: 0})

	started := time.Now()
	h.Responses(c)

	require.Equal(t, []int64{9920, 9921}, upstream.calls())
	require.Less(t, time.Since(started), 2500*time.Millisecond)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.completed")
}

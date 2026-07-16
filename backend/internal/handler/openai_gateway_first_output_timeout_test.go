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
	"github.com/tidwall/gjson"
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
	mu               sync.Mutex
	accountIDs       []int64
	sessionIDs       []string
	promptKeys       []string
	timeoutsBeforeOK int
}

type openAIBadGatewayRetryUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	sessionIDs []string
	promptKeys []string
}

func (u *openAIBadGatewayRetryUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	requestBody, _ := io.ReadAll(req.Body)
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.sessionIDs = append(u.sessionIDs, req.Header.Get("session_id"))
	u.promptKeys = append(u.promptKeys, gjson.GetBytes(requestBody, "prompt_cache_key").String())
	call := len(u.accountIDs)
	u.mu.Unlock()

	status := http.StatusOK
	body := `data: {"type":"response.completed","response":{"id":"resp_502_retry","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
	contentType := "text/event-stream"
	if call == 1 {
		status = http.StatusBadGateway
		body = `{"error":{"message":"temporary bad gateway"}}`
		contentType = "application/json"
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}, nil
}

func (u *openAIBadGatewayRetryUpstream) attempts() ([]int64, []string, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...), append([]string(nil), u.sessionIDs...), append([]string(nil), u.promptKeys...)
}

type openAIFirstOutputRetryConcurrencyCache struct {
	service.ConcurrencyCache
	mu           sync.Mutex
	acquireCalls map[int64]int
}

func (c *openAIFirstOutputRetryConcurrencyCache) AcquireAccountSlot(_ context.Context, accountID int64, _ int, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.acquireCalls == nil {
		c.acquireCalls = make(map[int64]int)
	}
	c.acquireCalls[accountID]++
	if accountID == 9930 {
		return c.acquireCalls[accountID] == 1, nil
	}
	return true, nil
}

func (c *openAIFirstOutputRetryConcurrencyCache) ReleaseAccountSlot(context.Context, int64, string) error {
	return nil
}

func (c *openAIFirstOutputRetryConcurrencyCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return false, nil
}

func (c *openAIFirstOutputRetryConcurrencyCache) DecrementAccountWaitCount(context.Context, int64) error {
	return nil
}

func (c *openAIFirstOutputRetryConcurrencyCache) GetAccountWaitingCount(context.Context, int64) (int, error) {
	return 0, nil
}

func (c *openAIFirstOutputRetryConcurrencyCache) GetAccountsLoadBatch(_ context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	result := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		result[account.ID] = &service.AccountLoadInfo{AccountID: account.ID}
	}
	return result, nil
}

func (u *openAIFirstOutputFailoverUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	requestBody, _ := io.ReadAll(req.Body)
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.sessionIDs = append(u.sessionIDs, req.Header.Get("session_id"))
	u.promptKeys = append(u.promptKeys, gjson.GetBytes(requestBody, "prompt_cache_key").String())
	call := len(u.accountIDs)
	timeoutsBeforeOK := u.timeoutsBeforeOK
	if timeoutsBeforeOK <= 0 {
		timeoutsBeforeOK = 1
	}
	u.mu.Unlock()

	body := io.ReadCloser(&openAIFirstOutputBlockingBody{closed: make(chan struct{})})
	if call > timeoutsBeforeOK {
		body = io.NopCloser(bytes.NewBufferString("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_failover\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}, nil
}

func (u *openAIFirstOutputFailoverUpstream) sessions() ([]string, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.sessionIDs...), append([]string(nil), u.promptKeys...)
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

func TestOpenAIFirstOutputFailoverUsesConfiguredAccountSwitchBudget(t *testing.T) {
	require.False(t, openAIAccountSwitchBudgetExhausted(0, 3))
	require.False(t, openAIAccountSwitchBudgetExhausted(2, 3))
	require.True(t, openAIAccountSwitchBudgetExhausted(3, 3))
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

func TestOpenAIRequestAllowsFirstOutputRetryRequiresReplayableInputAndConnectedClient(t *testing.T) {
	require.False(t, openAIRequestAllowsFirstOutputRetry(nil, []byte(`{"input":"hello"}`)))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	requestCtx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestCtx)

	require.True(t, openAIRequestAllowsFirstOutputRetry(c, []byte(`{"input":"hello"}`)))
	require.False(t, openAIRequestAllowsFirstOutputRetry(c, []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)))
	cancel()
	require.False(t, openAIRequestAllowsFirstOutputRetry(c, []byte(`{"input":"hello"}`)))
}

func TestOpenAIFailureAllowsFreshSessionRetryForBadGatewayOnly(t *testing.T) {
	require.True(t, openAIFailureAllowsFreshSessionRetry(&service.UpstreamFailoverError{FirstOutputTimeout: true}))
	require.True(t, openAIFailureAllowsFreshSessionRetry(&service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}))
	require.False(t, openAIFailureAllowsFreshSessionRetry(&service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}))
	require.False(t, openAIFailureAllowsFreshSessionRetry(nil))
}

func TestNewOpenAIBadGatewayRetrySessionUsesFreshHash(t *testing.T) {
	failure := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
	firstHash, firstBody, firstSeed, err := newOpenAIFreshSessionRetry([]byte(`{"input":"hello"}`), 1, failure)
	require.NoError(t, err)
	secondHash, secondBody, secondSeed, err := newOpenAIFreshSessionRetry([]byte(`{"input":"hello"}`), 2, failure)
	require.NoError(t, err)

	require.NotEmpty(t, firstHash)
	require.NotEmpty(t, secondHash)
	require.NotEqual(t, firstHash, secondHash)
	require.NotEqual(t, firstSeed, secondSeed)
	require.Equal(t, service.DeriveSessionHashFromSeed(firstSeed), firstHash)
	require.Equal(t, service.DeriveSessionHashFromSeed(secondSeed), secondHash)
	require.Equal(t, firstSeed, gjson.GetBytes(firstBody, "prompt_cache_key").String())
	require.Equal(t, secondSeed, gjson.GetBytes(secondBody, "prompt_cache_key").String())
}

func TestOpenAIResponsesBadGatewayRetriesSameAccountWithFreshSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4207)
	accounts := []service.Account{
		{
			ID: 9950, Name: "bad-gateway-oauth", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{"access_token": "token-1", "chatgpt_account_id": "account-1"},
		},
		{
			ID: 9951, Name: "fallback-oauth", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 2,
			Credentials: map[string]any{"access_token": "token-2", "chatgpt_account_id": "account-2"},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.OpenAIFirstOutputSameAccountRetries = 1
	cfg.Gateway.MaxAccountSwitches = 3

	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	upstream := &openAIBadGatewayRetryUpstream{}
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
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.2","input":"hello","stream":true,"prompt_cache_key":"original-session"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 1807, GroupID: &groupID,
		User:  &service.User{ID: 1707, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1707, Concurrency: 0})

	h.Responses(c)

	accountIDs, sessionIDs, promptKeys := upstream.attempts()
	require.Equal(t, []int64{9950, 9950}, accountIDs)
	require.Len(t, sessionIDs, 2)
	require.NotEmpty(t, sessionIDs[0])
	require.NotEmpty(t, sessionIDs[1])
	require.NotEqual(t, sessionIDs[0], sessionIDs[1])
	require.Equal(t, "original-session", promptKeys[0])
	require.Contains(t, promptKeys[1], "bad-gateway-retry:")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAIResponsesFirstOutputTimeoutRetriesSameAccountWithFreshSession(t *testing.T) {
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
	cfg.Gateway.OpenAIFirstOutputSameAccountRetries = 1
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

	require.Equal(t, []int64{9920, 9920}, upstream.calls())
	sessionIDs, promptKeys := upstream.sessions()
	require.Len(t, sessionIDs, 2)
	require.Len(t, promptKeys, 2)
	require.Empty(t, promptKeys[0])
	require.NotEmpty(t, sessionIDs[1])
	require.Contains(t, promptKeys[1], "first-output-retry:")
	require.Less(t, time.Since(started), 2500*time.Millisecond)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAIResponsesFirstOutputRetryQueueFullContinuesNormalFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4205)
	accounts := []service.Account{
		{
			ID: 9930, Name: "slow-oauth", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
			Credentials: map[string]any{"access_token": "token-1", "chatgpt_account_id": "account-1"},
		},
		{
			ID: 9931, Name: "fast-oauth", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 2, Concurrency: 1,
			Credentials: map[string]any{"access_token": "token-2", "chatgpt_account_id": "account-2"},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	cfg.Gateway.OpenAIFirstOutputSameAccountRetries = 1
	cfg.Gateway.MaxAccountSwitches = 3

	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	upstream := &openAIFirstOutputFailoverUpstream{}
	concurrencySvc := service.NewConcurrencyService(&openAIFirstOutputRetryConcurrencyCache{})
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrencySvc,
		service.NewBillingService(cfg, nil), nil, billingCacheSvc, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(
		gatewaySvc,
		concurrencySvc,
		billingCacheSvc,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.2","input":"hello","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 1805, GroupID: &groupID,
		User:  &service.User{ID: 1705, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1705, Concurrency: 0})

	h.Responses(c)

	require.Equal(t, []int64{9930, 9931}, upstream.calls())
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAIResponsesFirstOutputTimeoutUsesConfiguredFailoverBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4206)
	accounts := []service.Account{
		{
			ID: 9940, Name: "slow-oauth-1", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{"access_token": "token-1", "chatgpt_account_id": "account-1"},
		},
		{
			ID: 9941, Name: "slow-oauth-2", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 2,
			Credentials: map[string]any{"access_token": "token-2", "chatgpt_account_id": "account-2"},
		},
		{
			ID: 9942, Name: "fast-oauth", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 3,
			Credentials: map[string]any{"access_token": "token-3", "chatgpt_account_id": "account-3"},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	cfg.Gateway.OpenAIFirstOutputSameAccountRetries = 0
	cfg.Gateway.MaxAccountSwitches = 3

	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	upstream := &openAIFirstOutputFailoverUpstream{timeoutsBeforeOK: 2}
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
		ID: 1806, GroupID: &groupID,
		User:  &service.User{ID: 1706, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1706, Concurrency: 0})

	started := time.Now()
	h.Responses(c)

	require.Equal(t, []int64{9940, 9941, 9942}, upstream.calls())
	require.Less(t, time.Since(started), 3500*time.Millisecond)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "response.completed")
}

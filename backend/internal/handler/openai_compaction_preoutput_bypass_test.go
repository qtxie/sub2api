package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type delayedOpenAIAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r delayedOpenAIAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

func (r delayedOpenAIAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	accounts := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

type delayedOpenAIUpstreamRequest struct {
	path string
	body []byte
}

type delayedOpenAIUpstream struct {
	service.HTTPUpstream
	delay       time.Duration
	contentType string
	body        string

	mu       sync.Mutex
	requests []delayedOpenAIUpstreamRequest
}

func (u *delayedOpenAIUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.requests = append(u.requests, delayedOpenAIUpstreamRequest{
		path: req.URL.Path,
		body: append([]byte(nil), body...),
	})
	u.mu.Unlock()

	timer := time.NewTimer(u.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}

	contentType := u.contentType
	if contentType == "" {
		contentType = "application/json"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{contentType},
			"X-Request-Id": []string{"req_bypass"},
		},
		Body: io.NopCloser(bytes.NewBufferString(u.body)),
	}, nil
}

func (u *delayedOpenAIUpstream) lastRequest() (delayedOpenAIUpstreamRequest, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.requests) == 0 {
		return delayedOpenAIUpstreamRequest{}, false
	}
	request := u.requests[len(u.requests)-1]
	request.body = append([]byte(nil), request.body...)
	return request, true
}

func newDelayedOpenAIBypassHandler(t *testing.T, account service.Account, upstream service.HTTPUpstream) *OpenAIGatewayHandler {
	t.Helper()
	repo := delayedOpenAIAccountRepo{accounts: []service.Account{account}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	cfg.Gateway.OpenAIPreOutputDisconnectDrainSeconds = 1
	cfg.Gateway.OpenAIPostOutputBillingDrainSeconds = 1

	concurrencyService := service.NewConcurrencyService(nil)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gatewayService := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrencyService,
		nil, nil, billingCache, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)
}

func newDelayedOpenAIBypassContext(t *testing.T, body []byte, betaFeatures string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if betaFeatures != "" {
		c.Request.Header.Set("x-codex-beta-features", betaFeatures)
	}
	groupID := int64(901)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      901,
		GroupID: &groupID,
		Group: &service.Group{
			ID:               groupID,
			Platform:         service.PlatformOpenAI,
			SubscriptionType: service.SubscriptionTypeSubscription,
		},
		User: &service.User{ID: 901},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 901})
	return c, recorder
}

func TestResponsesNativeRemoteCompactionBypassesPreOutputWithoutLegacyCompactRouting(t *testing.T) {
	upstream := &delayedOpenAIUpstream{
		delay:       1200 * time.Millisecond,
		contentType: "text/event-stream",
		body: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_native_compact\"}}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_native_compact\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	}
	account := service.Account{
		ID:          91,
		Name:        "native-v2-only",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":          "oauth-token",
			"compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"},
		},
		Extra: map[string]any{"openai_compact_supported": false},
	}
	h := newDelayedOpenAIBypassHandler(t, account, upstream)
	body := []byte(`{"model":"gpt-5","stream":true,"input":[{"type":"message","role":"user","content":"hello"},{"type":"compaction_trigger"}]}`)
	c, recorder := newDelayedOpenAIBypassContext(t, body, "remote_compaction_v2")

	startedAt := time.Now()
	h.Responses(c)

	require.GreaterOrEqual(t, time.Since(startedAt), 1100*time.Millisecond)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "response.completed")
	require.NotContains(t, recorder.Body.String(), "first_output_timeout")
	request, ok := upstream.lastRequest()
	require.True(t, ok)
	require.Equal(t, "/backend-api/codex/responses", request.path)
	require.Equal(t, "gpt-5.4", gjson.GetBytes(request.body, "model").String())
	require.NotEqual(t, "gpt-5.4-openai-compact", gjson.GetBytes(request.body, "model").String())
	require.True(t, service.HasCompactionTriggerInInput(request.body))
}

func TestResponsesSyncBypassesPreOutputTimeout(t *testing.T) {
	upstream := &delayedOpenAIUpstream{
		delay:       1200 * time.Millisecond,
		contentType: "application/json",
		body:        `{"id":"resp_sync","object":"response","status":"completed","model":"gpt-5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
	}
	account := service.Account{
		ID:          92,
		Name:        "sync-account",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "oauth-token"},
	}
	h := newDelayedOpenAIBypassHandler(t, account, upstream)
	body := []byte(`{"model":"gpt-5","stream":false,"input":"hello"}`)
	c, recorder := newDelayedOpenAIBypassContext(t, body, "")

	startedAt := time.Now()
	h.Responses(c)

	require.GreaterOrEqual(t, time.Since(startedAt), 1100*time.Millisecond)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"id":"resp_sync"`)
	require.NotContains(t, recorder.Body.String(), "first_output_timeout")
	request, ok := upstream.lastRequest()
	require.True(t, ok)
	require.Equal(t, "/backend-api/codex/responses", request.path)
	require.True(t, gjson.GetBytes(request.body, "stream").Bool(), "OAuth SYNC responses are aggregated from an upstream SSE stream")
}

func TestResponsesPathCompactStillRequiresLegacyCompactCapability(t *testing.T) {
	upstream := &delayedOpenAIUpstream{}
	account := service.Account{
		ID:          93,
		Name:        "legacy-compact-unsupported",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra:       map[string]any{"openai_compact_supported": false},
	}
	h := newDelayedOpenAIBypassHandler(t, account, upstream)
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	c, recorder := newDelayedOpenAIBypassContext(t, body, "")
	c.Request.URL.Path = "/v1/responses/compact"

	h.Responses(c)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "compact_not_supported")
	_, forwarded := upstream.lastRequest()
	require.False(t, forwarded)
}

func TestResponsesRemoteCompactionHeaderWithoutTriggerStillUsesPreOutputTimeout(t *testing.T) {
	upstream := &delayedOpenAIUpstream{
		delay:       1200 * time.Millisecond,
		contentType: "text/event-stream",
		body:        "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_too_late\"}}\n\n",
	}
	account := service.Account{
		ID:          94,
		Name:        "ordinary-stream",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "oauth-token"},
	}
	h := newDelayedOpenAIBypassHandler(t, account, upstream)
	body := []byte(`{"model":"gpt-5","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	c, recorder := newDelayedOpenAIBypassContext(t, body, "remote_compaction_v2")

	h.Responses(c)

	require.Equal(t, http.StatusGatewayTimeout, recorder.Code)
	require.Contains(t, recorder.Body.String(), "first_output_timeout")
	_, forwarded := upstream.lastRequest()
	require.True(t, forwarded)
}

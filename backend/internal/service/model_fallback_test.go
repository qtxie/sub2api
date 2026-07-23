package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestNormalizeModelFallbackList(t *testing.T) {
	models, err := NormalizeModelFallbackList([]string{" model-b ", "model-a", "model-b"})
	if err != nil {
		t.Fatalf("NormalizeModelFallbackList() error = %v", err)
	}
	if want := []string{"model-b", "model-a"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("NormalizeModelFallbackList() = %v, want %v", models, want)
	}

	if _, err := NormalizeModelFallbackList([]string{"model-b", ""}); err == nil {
		t.Fatal("NormalizeModelFallbackList() should reject blank model names")
	}
	tooMany := make([]string, MaxModelFallbacks+1)
	for i := range tooMany {
		tooMany[i] = "model"
	}
	if _, err := NormalizeModelFallbackList(tooMany); err == nil {
		t.Fatal("NormalizeModelFallbackList() should reject oversized lists")
	}
}

func TestBuildModelFallbackChainUsesOrderedListAndLegacySetting(t *testing.T) {
	ctx := context.Background()
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback:  "true",
		SettingKeyFallbackModelsOpenAI: `["model-b","model-a","model-b"]`,
	}}, nil)
	if got, want := settings.BuildModelFallbackChain(ctx, PlatformOpenAI, "model-a"), []string{"model-a", "model-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildModelFallbackChain() = %v, want %v", got, want)
	}

	legacy := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback: "true",
		SettingKeyFallbackModelOpenAI: "legacy-model",
	}}, nil)
	if got, want := legacy.BuildModelFallbackChain(ctx, PlatformOpenAI, "requested"), []string{"requested", "legacy-model"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy BuildModelFallbackChain() = %v, want %v", got, want)
	}
}

func TestOpenAIModelFallbackStaysOnSelectedAccountAndPreservesOrder(t *testing.T) {
	ctx := context.Background()
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback:  "true",
		SettingKeyFallbackModelsOpenAI: `["model-b","model-c"]`,
	}}, nil)
	service := &OpenAIGatewayService{settingService: settings}
	account := &Account{ID: 42, Platform: PlatformOpenAI}
	body := []byte(`{"model":"model-a","input":"hello"}`)
	var attempts []string

	result, err := service.forwardWithSameAccountModelFallback(ctx, &gin.Context{}, account, body, func(attemptBody []byte) (*OpenAIForwardResult, error) {
		model := gjson.GetBytes(attemptBody, "model").String()
		attempts = append(attempts, model)
		if model != "model-c" {
			return nil, newModelUnavailableFailoverError(http.StatusNotFound, nil, []byte(`{"error":{"message":"model not found"}}`))
		}
		return &OpenAIForwardResult{Model: model, UpstreamModel: model}, nil
	})
	if err != nil {
		t.Fatalf("forwardWithSameAccountModelFallback() error = %v", err)
	}
	if want := []string{"model-a", "model-b", "model-c"}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	if result == nil || result.Model != "model-a" || result.UpstreamModel != "model-c" {
		t.Fatalf("result = %#v, want requested model model-a and upstream model model-c", result)
	}
	if account.ID != 42 {
		t.Fatalf("selected account changed during fallback: got %d", account.ID)
	}
}

func TestOpenAIModelFallbackStopsOnNonModelError(t *testing.T) {
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback:  "true",
		SettingKeyFallbackModelsOpenAI: `["model-b"]`,
	}}, nil)
	service := &OpenAIGatewayService{settingService: settings}
	wantErr := errors.New("upstream rate limited")
	attempts := 0

	_, err := service.forwardWithSameAccountModelFallback(context.Background(), &gin.Context{}, &Account{Platform: PlatformOpenAI}, []byte(`{"model":"model-a"}`), func([]byte) (*OpenAIForwardResult, error) {
		attempts++
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestOpenAISameAccountModelFallbackRetriesGatewayErrors(t *testing.T) {
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback:  "true",
		SettingKeyFallbackModelsOpenAI: `["model-b"]`,
	}}, nil)
	service := &OpenAIGatewayService{settingService: settings}

	for _, statusCode := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			var attempts []string
			result, err := service.forwardWithSameAccountModelFallback(context.Background(), &gin.Context{}, &Account{ID: 42, Platform: PlatformOpenAI}, []byte(`{"model":"model-a"}`), func(attemptBody []byte) (*OpenAIForwardResult, error) {
				model := gjson.GetBytes(attemptBody, "model").String()
				attempts = append(attempts, model)
				if model == "model-a" {
					return nil, newModelUnavailableFailoverError(statusCode, nil, []byte(`{"error":{"message":"upstream gateway error"}}`))
				}
				return &OpenAIForwardResult{Model: model, UpstreamModel: model}, nil
			})
			if err != nil {
				t.Fatalf("forwardWithSameAccountModelFallback() error = %v", err)
			}
			if want := []string{"model-a", "model-b"}; !reflect.DeepEqual(attempts, want) {
				t.Fatalf("attempts = %v, want %v", attempts, want)
			}
			if result == nil || result.Model != "model-a" || result.UpstreamModel != "model-b" {
				t.Fatalf("result = %#v, want requested model model-a and upstream model model-b", result)
			}
		})
	}
}

func TestShouldTriggerOpenAISameAccountModelFallback(t *testing.T) {
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback: "true",
	}}, nil)

	for _, statusCode := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		if !shouldTriggerOpenAISameAccountModelFallback(context.Background(), settings, statusCode, nil) {
			t.Errorf("status %d should trigger OpenAI same-account model fallback", statusCode)
		}
	}
	if shouldTriggerOpenAISameAccountModelFallback(context.Background(), settings, http.StatusInternalServerError, nil) {
		t.Error("status 500 should not trigger OpenAI same-account model fallback")
	}
	if !shouldTriggerOpenAISameAccountModelFallback(context.Background(), settings, http.StatusNotFound, []byte(`{"error":{"message":"model not found"}}`)) {
		t.Error("deterministic model-unavailable response should trigger OpenAI same-account model fallback")
	}
}

func TestShouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(t *testing.T) {
	modelNotFoundBody := []byte(`{"error":{"message":"model not found"}}`)
	if !shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(http.StatusNotFound, modelNotFoundBody) {
		t.Error("model-unavailable should record before same-account retry")
	}
	for _, statusCode := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		if shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(statusCode, []byte(`{"error":{"message":"busy"}}`)) {
			t.Errorf("status %d must defer account penalties until same-account chain exhausts", statusCode)
		}
	}
}

type gatewayFallbackAccountRepoStub struct {
	AccountRepository
	tempCalls       int
	modelLimitCalls int
}

func (r *gatewayFallbackAccountRepoStub) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempCalls++
	return nil
}

func (r *gatewayFallbackAccountRepoStub) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	r.modelLimitCalls++
	return nil
}

func TestOpenAISameAccountGatewayFallbackSuccessDoesNotPenalizeAccount(t *testing.T) {
	repo := &gatewayFallbackAccountRepoStub{}
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback:  "true",
		SettingKeyFallbackModelsOpenAI: `["model-b"]`,
	}}, nil)
	service := &OpenAIGatewayService{
		settingService:   settings,
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIGatewayFallbackTempAccount()

	result, err := service.forwardWithSameAccountModelFallback(
		context.Background(),
		&gin.Context{},
		account,
		[]byte(`{"model":"model-a"}`),
		func(attemptBody []byte) (*OpenAIForwardResult, error) {
			model := gjson.GetBytes(attemptBody, "model").String()
			if model == "model-a" {
				statusCode := http.StatusServiceUnavailable
				respBody := []byte(`{"error":{"message":"service unavailable"}}`)
				// Mirror production forward paths: only model-unavailable records early.
				if shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(statusCode, respBody) {
					service.recordOpenAISameAccountFallbackUpstreamError(
						context.Background(), account, statusCode, nil, respBody, model,
					)
				}
				return nil, newModelUnavailableFailoverError(statusCode, nil, respBody)
			}
			return &OpenAIForwardResult{Model: model, UpstreamModel: model}, nil
		},
	)
	if err != nil {
		t.Fatalf("forwardWithSameAccountModelFallback() error = %v", err)
	}
	if result == nil || result.UpstreamModel != "model-b" {
		t.Fatalf("result = %#v, want upstream model-b", result)
	}
	if repo.tempCalls != 0 || repo.modelLimitCalls != 0 {
		t.Fatalf("successful same-account gateway fallback penalized account (temp=%d model=%d)", repo.tempCalls, repo.modelLimitCalls)
	}
}

func TestOpenAISameAccountGatewayFallbackExhaustAppliesUpstreamError(t *testing.T) {
	repo := &gatewayFallbackAccountRepoStub{}
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback:  "true",
		SettingKeyFallbackModelsOpenAI: `["model-b"]`,
	}}, nil)
	service := &OpenAIGatewayService{
		settingService:   settings,
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIGatewayFallbackTempAccount()
	statusCode := http.StatusServiceUnavailable
	respBody := []byte(`{"error":{"message":"service unavailable"}}`)

	_, err := service.forwardWithSameAccountModelFallback(
		context.Background(),
		&gin.Context{},
		account,
		[]byte(`{"model":"model-a"}`),
		func(attemptBody []byte) (*OpenAIForwardResult, error) {
			model := gjson.GetBytes(attemptBody, "model").String()
			if shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(statusCode, respBody) {
				service.recordOpenAISameAccountFallbackUpstreamError(
					context.Background(), account, statusCode, nil, respBody, model,
				)
			}
			return nil, newModelUnavailableFailoverError(statusCode, nil, respBody)
		},
	)
	if err == nil {
		t.Fatal("expected exhausted same-account fallback error")
	}
	if repo.tempCalls+repo.modelLimitCalls != 1 {
		t.Fatalf("penalty calls temp=%d model=%d, want exactly 1 after same-account chain exhausts", repo.tempCalls, repo.modelLimitCalls)
	}
}

func openAIGatewayFallbackTempAccount() *Account {
	return &Account{
		ID:          77,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusServiceUnavailable),
					"keywords":         []any{"unavailable"},
					"duration_minutes": float64(10),
				},
			},
		},
	}
}

func TestMappedModelHintForFallbackAttempt(t *testing.T) {
	original := []byte(`{"model":"model-a"}`)
	if got := mappedModelHintForFallbackAttempt(original, original, "mapped-a"); got != "mapped-a" {
		t.Fatalf("primary mapped model hint = %q, want mapped-a", got)
	}
	if got := mappedModelHintForFallbackAttempt(original, []byte(`{"model":"model-b"}`), "mapped-a"); got != "" {
		t.Fatalf("fallback mapped model hint = %q, want empty so candidate mapping is recalculated", got)
	}
}

func TestIsUpstreamModelUnavailableErrorRejectsTransientFailures(t *testing.T) {
	if !IsUpstreamModelUnavailableError(http.StatusBadRequest, []byte(`{"error":{"message":"model is not supported on this account"}}`)) {
		t.Fatal("deterministic unsupported-model response should trigger fallback")
	}
	for _, test := range []struct {
		status int
		body   string
	}{
		{http.StatusTooManyRequests, `{"error":{"message":"model rate limit exceeded"}}`},
		{http.StatusServiceUnavailable, `{"error":{"message":"model is unavailable"}}`},
		{http.StatusNotFound, `{"error":{"message":"endpoint not found"}}`},
	} {
		if IsUpstreamModelUnavailableError(test.status, []byte(test.body)) {
			t.Fatalf("transient/non-model response unexpectedly triggered fallback: status=%d body=%s", test.status, test.body)
		}
	}
}

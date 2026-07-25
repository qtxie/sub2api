package service

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func newTestOpenAISoftMapStickyController(t *testing.T) *openAISoftMapStickyController {
	t.Helper()
	ctrl := newOpenAISoftMapStickyControllerWithExecutor(
		nil,
		openAISoftMapStickyConfig{
			stickyEnabled:        true,
			probeEnabled:         true,
			defaultProbeInterval: 2 * time.Minute,
			probeIntervalStep:    3 * time.Minute,
			maxProbeInterval:     26 * time.Minute,
			probeTimeout:         20 * time.Second,
			maxTTFT:              20 * time.Second,
			minHealthyRequests:   3,
			probation:            5 * time.Minute,
		},
		nil,
	)
	t.Cleanup(ctrl.stopBackgroundProbes)
	return ctrl
}

func TestOpenAISoftMapStickyEntersOnSoftSuccessAndSkipsPrimaryNextRequest(t *testing.T) {
	ctrl := newTestOpenAISoftMapStickyController(t)
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback: "false",
	}}, nil)
	account := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.4",
			},
		},
	}
	service := &OpenAIGatewayService{
		settingService:      settings,
		openaiSoftMapSticky: ctrl,
	}

	var firstAttempts []string
	result, err := service.forwardWithSameAccountModelFallback(
		context.Background(),
		&gin.Context{},
		account,
		[]byte(`{"model":"gpt-5.5"}`),
		func(attemptBody []byte) (*OpenAIForwardResult, error) {
			model := gjson.GetBytes(attemptBody, "model").String()
			firstAttempts = append(firstAttempts, model)
			if model == "gpt-5.5" {
				return nil, newModelUnavailableFailoverError(
					http.StatusNotFound,
					nil,
					[]byte(`{"error":{"message":"model not found"}}`),
				)
			}
			return &OpenAIForwardResult{Model: model, UpstreamModel: model}, nil
		},
	)
	if err != nil {
		t.Fatalf("first forward error = %v", err)
	}
	if want := []string{"gpt-5.5", "gpt-5.4"}; !reflect.DeepEqual(firstAttempts, want) {
		t.Fatalf("first attempts = %v, want %v", firstAttempts, want)
	}
	if result == nil || result.Model != "gpt-5.5" || result.UpstreamModel != "gpt-5.4" {
		t.Fatalf("first result = %#v", result)
	}

	plan := ctrl.resolveAttemptPlan(context.Background(), account, "gpt-5.5", []string{"gpt-5.5", "gpt-5.4"})
	if plan.phase != openAISoftMapPhaseSticky || plan.active != "gpt-5.4" {
		t.Fatalf("sticky plan = %#v, want sticky/gpt-5.4", plan)
	}
	if want := []string{"gpt-5.4"}; !reflect.DeepEqual(plan.chain, want) {
		t.Fatalf("sticky chain = %v, want %v", plan.chain, want)
	}

	var secondAttempts []string
	result, err = service.forwardWithSameAccountModelFallback(
		context.Background(),
		&gin.Context{},
		account,
		[]byte(`{"model":"gpt-5.5"}`),
		func(attemptBody []byte) (*OpenAIForwardResult, error) {
			model := gjson.GetBytes(attemptBody, "model").String()
			secondAttempts = append(secondAttempts, model)
			return &OpenAIForwardResult{Model: model, UpstreamModel: model}, nil
		},
	)
	if err != nil {
		t.Fatalf("second forward error = %v", err)
	}
	if want := []string{"gpt-5.4"}; !reflect.DeepEqual(secondAttempts, want) {
		t.Fatalf("second attempts = %v, want sticky mapped-only %v", secondAttempts, want)
	}
	if result == nil || result.Model != "gpt-5.5" || result.UpstreamModel != "gpt-5.4" {
		t.Fatalf("second result = %#v", result)
	}
}

func TestOpenAISoftMapStickyClearsWhenMappedFails(t *testing.T) {
	ctrl := newTestOpenAISoftMapStickyController(t)
	account := &Account{
		ID:       78,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{"gpt-5.5": "gpt-5.4"},
		},
	}
	ctrl.enterSticky(context.Background(), account.ID, "gpt-5.5", "gpt-5.4", []string{"gpt-5.4"}, "seed")

	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback: "false",
	}}, nil)
	service := &OpenAIGatewayService{settingService: settings, openaiSoftMapSticky: ctrl}
	wantErr := newModelUnavailableFailoverError(
		http.StatusNotFound,
		nil,
		[]byte(`{"error":{"message":"model not found"}}`),
	)

	_, err := service.forwardWithSameAccountModelFallback(
		context.Background(),
		&gin.Context{},
		account,
		[]byte(`{"model":"gpt-5.5"}`),
		func(attemptBody []byte) (*OpenAIForwardResult, error) {
			return nil, wantErr
		},
	)
	if err == nil {
		t.Fatal("expected mapped failure")
	}
	plan := ctrl.resolveAttemptPlan(context.Background(), account, "gpt-5.5", []string{"gpt-5.5", "gpt-5.4"})
	if plan.phase != "" {
		t.Fatalf("expected sticky cleared, plan=%#v", plan)
	}
}

func TestOpenAISoftMapProbationTriesPrimaryThenMapped(t *testing.T) {
	ctrl := newTestOpenAISoftMapStickyController(t)
	account := &Account{
		ID:       79,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{"gpt-5.5": "gpt-5.4"},
		},
	}
	ctrl.enterSticky(context.Background(), account.ID, "gpt-5.5", "gpt-5.4", []string{"gpt-5.4"}, "seed")
	if !ctrl.recordProbeSuccess(context.Background(), account.ID, "gpt-5.5", 100) {
		t.Fatal("probe success should enter probation")
	}

	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableModelFallback: "false",
	}}, nil)
	service := &OpenAIGatewayService{settingService: settings, openaiSoftMapSticky: ctrl}

	var attempts []string
	result, err := service.forwardWithSameAccountModelFallback(
		context.Background(),
		&gin.Context{},
		account,
		[]byte(`{"model":"gpt-5.5"}`),
		func(attemptBody []byte) (*OpenAIForwardResult, error) {
			model := gjson.GetBytes(attemptBody, "model").String()
			attempts = append(attempts, model)
			if model == "gpt-5.5" {
				return nil, &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}
			}
			return &OpenAIForwardResult{Model: model, UpstreamModel: model}, nil
		},
	)
	if err != nil {
		t.Fatalf("forward error = %v", err)
	}
	if want := []string{"gpt-5.5", "gpt-5.4"}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempts = %v, want primary then mapped", attempts)
	}
	if result == nil || result.UpstreamModel != "gpt-5.4" {
		t.Fatalf("result = %#v", result)
	}
	plan := ctrl.resolveAttemptPlan(context.Background(), account, "gpt-5.5", []string{"gpt-5.5", "gpt-5.4"})
	if plan.phase != openAISoftMapPhaseSticky {
		t.Fatalf("probation primary failure should relapse to sticky, plan=%#v", plan)
	}
}

func TestOpenAISoftMapProbationRestoresAfterHealthyPrimarySuccesses(t *testing.T) {
	ctrl := newTestOpenAISoftMapStickyController(t)
	now := time.Unix(1_700_000_000, 0)
	ctrl.now = func() time.Time { return now }

	accountID := int64(80)
	ctrl.enterSticky(context.Background(), accountID, "gpt-5.5", "gpt-5.4", []string{"gpt-5.4"}, "seed")
	if !ctrl.recordProbeSuccess(context.Background(), accountID, "gpt-5.5", 50) {
		t.Fatal("expected probation")
	}

	// Expire probation window so healthy request count alone can restore.
	now = now.Add(6 * time.Minute)
	fast := 40
	for i := 0; i < 3; i++ {
		ctrl.recordPrimarySuccess(context.Background(), accountID, "gpt-5.5", &fast)
	}

	account := &Account{
		ID:       accountID,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{"gpt-5.5": "gpt-5.4"},
		},
	}
	plan := ctrl.resolveAttemptPlan(context.Background(), account, "gpt-5.5", []string{"gpt-5.5", "gpt-5.4"})
	if plan.phase != "" {
		t.Fatalf("expected full restore to active primary, plan=%#v", plan)
	}
	if want := []string{"gpt-5.5", "gpt-5.4"}; !reflect.DeepEqual(plan.chain, want) {
		t.Fatalf("restored chain = %v, want %v", plan.chain, want)
	}
}

func TestOpenAISoftMapStickyTriggerIncludes429(t *testing.T) {
	err429 := &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}
	if !isOpenAISoftMapStickyTriggerError(err429) {
		t.Fatal("429 must be a sticky trigger")
	}
	softAccount := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{"model-a": "model-b"},
		},
	}
	if !shouldTriggerOpenAISameAccountModelFallback(
		context.Background(),
		NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
			SettingKeyEnableModelFallback: "false",
		}}, nil),
		softAccount,
		"model-a",
		http.StatusTooManyRequests,
		nil,
	) {
		t.Fatal("429 should trigger same-account model fallback for soft-mapped models")
	}
	if shouldTriggerOpenAISameAccountModelFallback(
		context.Background(),
		NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
			SettingKeyEnableModelFallback:  "true",
			SettingKeyFallbackModelsOpenAI: `["model-b"]`,
		}}, nil),
		&Account{Platform: PlatformOpenAI},
		"model-a",
		http.StatusTooManyRequests,
		nil,
	) {
		t.Fatal("429 must not trigger same-account fallback without soft mapping")
	}
}

func TestOpenAISoftMapStickyClearsWhenSoftTargetsRemoved(t *testing.T) {
	ctrl := newTestOpenAISoftMapStickyController(t)
	account := &Account{
		ID:       81,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{"gpt-5.5": "gpt-5.4"},
		},
	}
	ctrl.enterSticky(context.Background(), account.ID, "gpt-5.5", "gpt-5.4", []string{"gpt-5.4"}, "seed")
	account.Credentials = map[string]any{}

	plan := ctrl.resolveAttemptPlan(context.Background(), account, "gpt-5.5", []string{"gpt-5.5"})
	if plan.phase != "" {
		t.Fatalf("expected sticky cleared after soft targets removed, plan=%#v", plan)
	}
	// Re-adding mapping must not revive the cleared sticky winner.
	account.Credentials = map[string]any{
		"soft_model_mapping": map[string]any{"gpt-5.5": "gpt-5.4"},
	}
	plan = ctrl.resolveAttemptPlan(context.Background(), account, "gpt-5.5", []string{"gpt-5.5", "gpt-5.4"})
	if plan.phase != "" {
		t.Fatalf("cleared sticky must stay cleared, plan=%#v", plan)
	}
}

func TestOpenAISoftMapProbationIgnoresNilFirstTokenMs(t *testing.T) {
	ctrl := newTestOpenAISoftMapStickyController(t)
	now := time.Unix(1_700_000_000, 0)
	ctrl.now = func() time.Time { return now }

	accountID := int64(82)
	ctrl.enterSticky(context.Background(), accountID, "gpt-5.5", "gpt-5.4", []string{"gpt-5.4"}, "seed")
	if !ctrl.recordProbeSuccess(context.Background(), accountID, "gpt-5.5", 50) {
		t.Fatal("expected probation")
	}
	now = now.Add(6 * time.Minute)
	for i := 0; i < 3; i++ {
		ctrl.recordPrimarySuccess(context.Background(), accountID, "gpt-5.5", nil)
	}

	account := &Account{
		ID:       accountID,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"soft_model_mapping": map[string]any{"gpt-5.5": "gpt-5.4"},
		},
	}
	plan := ctrl.resolveAttemptPlan(context.Background(), account, "gpt-5.5", []string{"gpt-5.5", "gpt-5.4"})
	if plan.phase != openAISoftMapPhaseProbation {
		t.Fatalf("nil FirstTokenMs must not restore primary, plan=%#v", plan)
	}
}

func TestOpenAISoftMapStickyControllerStopsBackgroundProbesOnClose(t *testing.T) {
	ctrl := newTestOpenAISoftMapStickyController(t)
	svc := &OpenAIGatewayService{openaiSoftMapSticky: ctrl}
	svc.CloseOpenAIWSPool()
	if ctrl.probeCtx.Err() == nil {
		t.Fatal("expected soft-map probe context canceled on CloseOpenAIWSPool")
	}
}

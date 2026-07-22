package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	MaxModelFallbacks    = 8
	maxFallbackModelName = 200
)

const GatewayFailureReasonModelUnavailable = GatewayFailureReason("model_unavailable")

// NormalizeModelFallbackList validates an ordered fallback chain while
// preserving administrator-defined order.
func NormalizeModelFallbackList(models []string) ([]string, error) {
	if len(models) > MaxModelFallbacks {
		return nil, fmt.Errorf("at most %d fallback models are allowed", MaxModelFallbacks)
	}
	normalized := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			return nil, fmt.Errorf("fallback model names cannot be empty")
		}
		if len(model) > maxFallbackModelName {
			return nil, fmt.Errorf("fallback model names cannot exceed %d bytes", maxFallbackModelName)
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		normalized = append(normalized, model)
	}
	if normalized == nil {
		return []string{}, nil
	}
	return normalized, nil
}

func parseStoredFallbackModels(settings map[string]string, listKey, legacyModel string) []string {
	if raw, ok := settings[listKey]; ok {
		var models []string
		if err := json.Unmarshal([]byte(raw), &models); err == nil {
			if normalized, normalizeErr := NormalizeModelFallbackList(models); normalizeErr == nil {
				return normalized
			}
		}
	}
	legacyModel = strings.TrimSpace(legacyModel)
	if legacyModel == "" {
		return []string{}
	}
	return []string{legacyModel}
}

func firstFallbackModel(models []string) string {
	if len(models) > 0 {
		return strings.TrimSpace(models[0])
	}
	return ""
}

func fallbackSettingKeys(platform string) (listKey, legacyKey, defaultModel string) {
	switch platform {
	case PlatformAnthropic:
		return SettingKeyFallbackModelsAnthropic, SettingKeyFallbackModelAnthropic, "claude-3-5-sonnet-20241022"
	case PlatformOpenAI:
		return SettingKeyFallbackModelsOpenAI, SettingKeyFallbackModelOpenAI, "gpt-4o"
	case PlatformGemini:
		return SettingKeyFallbackModelsGemini, SettingKeyFallbackModelGemini, "gemini-2.5-pro"
	case PlatformAntigravity:
		return SettingKeyFallbackModelsAntigravity, SettingKeyFallbackModelAntigravity, "gemini-2.5-pro"
	default:
		return "", "", ""
	}
}

// GetFallbackModels returns the configured same-account fallback chain for a
// platform. It reads the legacy singular setting when upgrading an older DB.
func (s *SettingService) GetFallbackModels(ctx context.Context, platform string) []string {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	listKey, legacyKey, defaultModel := fallbackSettingKeys(platform)
	if listKey == "" {
		return nil
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{listKey, legacyKey})
	if err != nil {
		return nil
	}
	legacyModel := strings.TrimSpace(values[legacyKey])
	if legacyModel == "" {
		legacyModel = defaultModel
	}
	return parseStoredFallbackModels(values, listKey, legacyModel)
}

// BuildModelFallbackChain prepends the requested model and removes duplicate
// fallback entries. The returned chain always contains requestedModel when it
// is non-empty, even when fallback is disabled or settings cannot be loaded.
func (s *SettingService) BuildModelFallbackChain(ctx context.Context, platform, requestedModel string) []string {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	chain := []string{requestedModel}
	if s == nil || !s.IsModelFallbackEnabled(ctx) {
		return chain
	}
	for _, model := range s.GetFallbackModels(ctx, platform) {
		if model != requestedModel {
			chain = append(chain, model)
		}
	}
	return chain
}

// IsUpstreamModelUnavailableError only matches deterministic model capability
// failures. Transient capacity, rate-limit, and generic provider errors must
// continue through their existing retry paths.
func IsUpstreamModelUnavailableError(statusCode int, body []byte) bool {
	if statusCode < http.StatusBadRequest || statusCode >= http.StatusInternalServerError ||
		statusCode == http.StatusTooManyRequests || len(body) == 0 {
		return false
	}
	normalized := normalizeModelNotFoundBody(body)
	if normalized == "" || !strings.Contains(normalized, "model") {
		return false
	}
	if statusCode == http.StatusNotFound && strings.Contains(normalized, "not found") {
		return true
	}
	for _, marker := range []string{
		"model not found",
		"unknown model",
		"model does not exist",
		"model is not supported",
		"model not supported",
		"not supported",
		"unsupported",
		"model unavailable",
		"model is unavailable",
		"not a valid model",
		"invalid model",
		"does not have access to",
		"do not have access to",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func shouldTriggerModelFallback(ctx context.Context, settings *SettingService, statusCode int, body []byte) bool {
	return settings != nil && settings.IsModelFallbackEnabled(ctx) && IsUpstreamModelUnavailableError(statusCode, body)
}

func newModelUnavailableFailoverError(statusCode int, headers http.Header, body []byte) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:        statusCode,
		ResponseBody:      body,
		ResponseHeaders:   headers.Clone(),
		Scope:             GatewayFailureScopeAccount,
		Reason:            GatewayFailureReasonModelUnavailable,
		NextAccountAction: NextAccountRetry,
	}
}

func IsModelUnavailableFailover(err error) bool {
	var failoverErr *UpstreamFailoverError
	return errors.As(err, &failoverErr) && failoverErr.Reason == GatewayFailureReasonModelUnavailable
}

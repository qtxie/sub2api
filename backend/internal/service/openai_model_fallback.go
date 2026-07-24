package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openAIModelForward func(body []byte) (*OpenAIForwardResult, error)

// shouldTriggerOpenAISameAccountModelFallback decides whether the selected
// OpenAI account should try its configured fallback models before the handler
// moves on to another account.
func shouldTriggerOpenAISameAccountModelFallback(ctx context.Context, settings *SettingService, statusCode int, body []byte) bool {
	if settings == nil || !settings.IsModelFallbackEnabled(ctx) {
		return false
	}
	if IsUpstreamModelUnavailableError(statusCode, body) {
		return true
	}
	switch statusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// shouldRetryOpenAISameAccountModelFallback recognizes both upstream responses
// classified as model-unavailable and synthetic gateway failures produced before
// any semantic output (for example first-output and response-header timeouts).
func shouldRetryOpenAISameAccountModelFallback(ctx context.Context, settings *SettingService, err error) bool {
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) || failoverErr == nil {
		return false
	}
	if !failoverErr.ShouldRetryNextAccount() {
		return false
	}
	if failoverErr.Reason == GatewayFailureReasonPersistentTransport {
		// A durable proxy, DNS, or routing failure can only recover on another account.
		return false
	}
	if IsModelUnavailableFailover(err) {
		return true
	}
	return shouldTriggerOpenAISameAccountModelFallback(
		ctx,
		settings,
		failoverErr.StatusCode,
		failoverErr.ResponseBody,
	)
}

// shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry reports whether
// HandleUpstreamError is safe to run before trying other models on the same
// account. Deterministic model-unavailable failures are model-scoped. Gateway
// errors (502/503/504) can temp-unschedule the whole account and must wait until
// the same-account chain is exhausted.
func shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(statusCode int, body []byte) bool {
	return IsUpstreamModelUnavailableError(statusCode, body)
}

func (s *OpenAIGatewayService) recordOpenAISameAccountFallbackUpstreamError(
	ctx context.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	body []byte,
	requestedModel string,
) {
	if s == nil || s.rateLimitService == nil || account == nil {
		return
	}
	s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, headers, body, requestedModel)
}

func recordOpenAISameAccountFallbackUpstreamErrorFromFailover(
	ctx context.Context,
	s *OpenAIGatewayService,
	account *Account,
	err error,
	requestedModel string,
) {
	var failoverErr *UpstreamFailoverError
	if s == nil || account == nil || !errors.As(err, &failoverErr) || failoverErr == nil {
		return
	}
	if failoverErr.Reason == GatewayFailureReasonFirstOutputTimeout {
		// First-output timeouts apply their stream-timeout policy at the source.
		return
	}
	if shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(failoverErr.StatusCode, failoverErr.ResponseBody) {
		// Already applied when the attempt returned the same-account failover error.
		return
	}
	s.recordOpenAISameAccountFallbackUpstreamError(
		ctx,
		account,
		failoverErr.StatusCode,
		failoverErr.ResponseHeaders,
		failoverErr.ResponseBody,
		requestedModel,
	)
}

func mappedModelHintForFallbackAttempt(originalBody, attemptBody []byte, defaultMappedModel string) string {
	originalModel := strings.TrimSpace(gjson.GetBytes(originalBody, "model").String())
	attemptModel := strings.TrimSpace(gjson.GetBytes(attemptBody, "model").String())
	if originalModel != "" && attemptModel != "" && attemptModel != originalModel {
		return ""
	}
	return defaultMappedModel
}

// openAIModelFallbackWriteSafety tracks whether same-account model retries are still
// safe on the current client response, and preserves SafeToFailoverAfterWrite when
// only keepalive-style bytes were emitted.
type openAIModelFallbackWriteSafety struct {
	lastWrittenSize int
	sawSafeWrite    bool
	sawUnsafeWrite  bool
}

func newOpenAIModelFallbackWriteSafety(c *gin.Context) openAIModelFallbackWriteSafety {
	return openAIModelFallbackWriteSafety{lastWrittenSize: OpenAICompactKeepaliveAdjustedWrittenSize(c)}
}

func (s *openAIModelFallbackWriteSafety) observe(c *gin.Context, err error) {
	if s == nil || err == nil {
		return
	}
	writtenSize := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	if writtenSize == s.lastWrittenSize {
		return
	}
	s.lastWrittenSize = writtenSize

	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) && failoverErr != nil && failoverErr.SafeToFailoverAfterWrite {
		s.sawSafeWrite = true
		return
	}
	// Semantic (or otherwise unmarked) bytes already went to the client. Another
	// model attempt on this same response would splice two streams together.
	s.sawUnsafeWrite = true
}

func (s *openAIModelFallbackWriteSafety) allowsModelRetry() bool {
	return s == nil || !s.sawUnsafeWrite
}

func (s *openAIModelFallbackWriteSafety) preserve(err error) error {
	if s == nil || !s.sawSafeWrite || s.sawUnsafeWrite {
		return err
	}
	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) && failoverErr != nil {
		failoverErr.SafeToFailoverAfterWrite = true
	}
	return err
}

func (s *OpenAIGatewayService) forwardWithSameAccountModelFallback(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	forward openAIModelForward,
) (*OpenAIForwardResult, error) {
	requestedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	var settings *SettingService
	if s != nil {
		settings = s.settingService
	}
	writeSafety := newOpenAIModelFallbackWriteSafety(c)
	result, err := forward(body)
	writeSafety.observe(c, err)
	if err == nil || requestedModel == "" || account == nil ||
		!writeSafety.allowsModelRetry() ||
		!shouldRetryOpenAISameAccountModelFallback(ctx, settings, err) {
		return result, writeSafety.preserve(err)
	}

	chain := []string{requestedModel}
	if settings != nil {
		chain = settings.BuildModelFallbackChain(ctx, account.Platform, requestedModel)
	}
	lastErr := err
	for _, candidate := range chain[1:] {
		result, err = forward(ReplaceModelInBody(body, candidate))
		writeSafety.observe(c, err)
		if err == nil {
			if result != nil {
				actualModel := strings.TrimSpace(result.UpstreamModel)
				if actualModel == "" {
					actualModel = candidate
				}
				if strings.TrimSpace(result.BillingModel) == "" {
					result.BillingModel = actualModel
				}
				result.Model = requestedModel
				result.UpstreamModel = actualModel
			}
			return result, nil
		}
		lastErr = err
		if !writeSafety.allowsModelRetry() ||
			!shouldRetryOpenAISameAccountModelFallback(ctx, settings, err) {
			return nil, writeSafety.preserve(err)
		}
	}
	// Gateway 502/503/504 deferred rate-limit handling: only penalize after the
	// same-account chain cannot recover (successful fallback must not unsched the account).
	lastErr = writeSafety.preserve(lastErr)
	recordOpenAISameAccountFallbackUpstreamErrorFromFailover(ctx, s, account, lastErr, requestedModel)
	return nil, lastErr
}

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

// GatewayFailureReasonSameAccountModelRetry marks a non-model-unavailable
// upstream failure that should first try other models on the same account.
const GatewayFailureReasonSameAccountModelRetry = GatewayFailureReason("same_account_model_retry")

type openAIDeferredModelUnavailable struct {
	statusCode int
	headers    http.Header
	body       []byte
}

// newOpenAISameAccountModelFallbackError builds the failover error used when a
// forward path wants the same-account model chain to run. Deterministic model
// capability failures keep the model_unavailable reason; transient gateway and
// rate-limit failures use a distinct reason so handlers/observability do not
// treat them as model capability gaps.
func newOpenAISameAccountModelFallbackError(statusCode int, headers http.Header, body []byte) *UpstreamFailoverError {
	if IsUpstreamModelUnavailableError(statusCode, body) {
		return newModelUnavailableFailoverError(statusCode, headers, body)
	}
	var cloned http.Header
	if headers != nil {
		cloned = headers.Clone()
	}
	return &UpstreamFailoverError{
		StatusCode:        statusCode,
		ResponseBody:      body,
		ResponseHeaders:   cloned,
		Scope:             GatewayFailureScopeAccount,
		Reason:            GatewayFailureReasonSameAccountModelRetry,
		NextAccountAction: NextAccountRetry,
	}
}

// shouldTriggerOpenAISameAccountModelFallback decides whether the selected
// OpenAI account should try its configured fallback models before the handler
// moves on to another account. Soft mapping candidates enable this path even
// when global enable_model_fallback is off.
func shouldTriggerOpenAISameAccountModelFallback(
	ctx context.Context,
	settings *SettingService,
	account *Account,
	requestedModel string,
	statusCode int,
	body []byte,
) bool {
	platform := ""
	if account != nil {
		platform = account.Platform
	}
	if !hasSameAccountModelFallbackCandidates(ctx, settings, account, platform, requestedModel) {
		return false
	}
	if IsUpstreamModelUnavailableError(statusCode, body) {
		return true
	}
	switch statusCode {
	case http.StatusTooManyRequests:
		// Account-wide 429s waste a full extra attempt on global fallbacks.
		// Soft-mapped targets can still absorb model-scoped pressure on the
		// same account, so only soft mapping opts into 429 same-account retry.
		return account != nil && account.HasSoftModelFallbacks(requestedModel)
	case http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// shouldRetryOpenAISameAccountModelFallback recognizes both upstream responses
// classified as model-unavailable and synthetic gateway failures produced before
// any semantic output (for example first-output and response-header timeouts).
func shouldRetryOpenAISameAccountModelFallback(
	ctx context.Context,
	settings *SettingService,
	account *Account,
	requestedModel string,
	err error,
) bool {
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
		return hasSameAccountModelFallbackCandidates(ctx, settings, account, accountPlatformOrEmpty(account), requestedModel)
	}
	if failoverErr.IsOpenAIPreOutputTimeout() {
		return hasSameAccountModelFallbackCandidates(ctx, settings, account, accountPlatformOrEmpty(account), requestedModel)
	}
	return shouldTriggerOpenAISameAccountModelFallback(
		ctx,
		settings,
		account,
		requestedModel,
		failoverErr.StatusCode,
		failoverErr.ResponseBody,
	)
}

func accountPlatformOrEmpty(account *Account) string {
	if account == nil {
		return ""
	}
	return account.Platform
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
	// Soft-mapped sources own recovery via sticky/probe state. Defer model-not-found
	// cooldowns until the same-account soft chain is exhausted so sticky routing
	// can keep the account schedulable for the request model.
	if shouldDeferOpenAISameAccountModelUnavailableRecording(account, requestedModel) &&
		IsUpstreamModelUnavailableError(statusCode, body) {
		return
	}
	s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, headers, body, requestedModel)
}

func (s *OpenAIGatewayService) recordOpenAISameAccountModelUnavailableAfterSoftChain(
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
	if !IsUpstreamModelUnavailableError(statusCode, body) {
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
	deferredModelUnavailableFlushed bool,
) {
	var failoverErr *UpstreamFailoverError
	if s == nil || account == nil || !errors.As(err, &failoverErr) || failoverErr == nil {
		return
	}
	if failoverErr.Reason == GatewayFailureReasonFirstOutputTimeout {
		// First-output timeouts apply their stream-timeout policy at the source.
		return
	}
	if failoverErr.AccountStateHandled {
		return
	}
	if shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(failoverErr.StatusCode, failoverErr.ResponseBody) {
		// Non-soft paths already recorded model-unavailable before same-account retry.
		// Soft-mapped sources deferred that write; the primary miss is flushed via
		// deferred tracking. Only flush lastErr here when nothing was deferred.
		if !deferredModelUnavailableFlushed &&
			shouldDeferOpenAISameAccountModelUnavailableRecording(account, requestedModel) {
			s.recordOpenAISameAccountModelUnavailableAfterSoftChain(
				ctx,
				account,
				failoverErr.StatusCode,
				failoverErr.ResponseHeaders,
				failoverErr.ResponseBody,
				requestedModel,
			)
		}
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

func captureOpenAIDeferredPrimaryModelUnavailable(
	account *Account,
	requestedModel string,
	isPrimary bool,
	err error,
) *openAIDeferredModelUnavailable {
	if !isPrimary || err == nil ||
		!shouldDeferOpenAISameAccountModelUnavailableRecording(account, requestedModel) {
		return nil
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) || failoverErr == nil {
		return nil
	}
	if !shouldRecordOpenAISameAccountFallbackUpstreamErrorBeforeRetry(failoverErr.StatusCode, failoverErr.ResponseBody) {
		return nil
	}
	var headers http.Header
	if failoverErr.ResponseHeaders != nil {
		headers = failoverErr.ResponseHeaders.Clone()
	}
	return &openAIDeferredModelUnavailable{
		statusCode: failoverErr.StatusCode,
		headers:    headers,
		body:       append([]byte(nil), failoverErr.ResponseBody...),
	}
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

func finalizeOpenAISameAccountFallbackSuccess(
	result *OpenAIForwardResult,
	requestedModel, candidate string,
) *OpenAIForwardResult {
	if result == nil {
		return nil
	}
	actualModel := strings.TrimSpace(result.UpstreamModel)
	if actualModel == "" {
		actualModel = candidate
	}
	if strings.TrimSpace(result.BillingModel) == "" {
		result.BillingModel = actualModel
	}
	result.Model = requestedModel
	result.UpstreamModel = actualModel
	return result
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

	baseChain := BuildSameAccountModelFallbackChain(ctx, settings, account, accountPlatformOrEmpty(account), requestedModel)
	if len(baseChain) == 0 && requestedModel != "" {
		baseChain = []string{requestedModel}
	}

	stickyCtrl := s.getOpenAISoftMapStickyController()
	plan := openAISoftMapAttemptPlan{primary: requestedModel, chain: baseChain}
	if stickyCtrl != nil && account != nil && requestedModel != "" {
		stateCtx, stateCancel := context.WithTimeout(ctx, openAISoftMapStoreTimeout)
		plan = stickyCtrl.resolveAttemptPlan(stateCtx, account, requestedModel, baseChain)
		stateCancel()
		if plan.shouldProbe {
			s.scheduleOpenAISoftMapPrimaryProbe(stickyCtrl, account, requestedModel)
		}
	}
	chain := plan.chain
	if len(chain) == 0 {
		if requestedModel == "" {
			result, err := forward(body)
			writeSafety.observe(c, err)
			return result, writeSafety.preserve(err)
		}
		chain = []string{requestedModel}
	}

	var softTargets []string
	if account != nil {
		softTargets = account.GetSoftModelFallbacks(requestedModel)
	}
	softSet := make(map[string]struct{}, len(softTargets)+1)
	for _, model := range softTargets {
		softSet[model] = struct{}{}
	}
	if plan.active != "" {
		softSet[plan.active] = struct{}{}
	}
	isSoftCandidate := func(model string) bool {
		_, ok := softSet[strings.TrimSpace(model)]
		return ok
	}

	var (
		lastResult                 *OpenAIForwardResult
		lastErr                    error
		primaryFailedWithTrigger   bool
		triedPrimary               bool
		stickyStateTouched         bool
		deferredPrimaryUnavailable *openAIDeferredModelUnavailable
	)
	clearStickyOnExit := plan.phase == openAISoftMapPhaseSticky || plan.phase == openAISoftMapPhaseProbation

	for index, candidate := range chain {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if lastErr != nil {
			if !writeSafety.allowsModelRetry() ||
				!shouldRetryOpenAISameAccountModelFallback(ctx, settings, account, requestedModel, lastErr) {
				break
			}
		} else if index > 0 {
			// Leading empty candidates were skipped; do not invent retries without a failure.
			break
		}

		attemptBody := body
		if candidate != requestedModel {
			attemptBody = ReplaceModelInBody(body, candidate)
		}
		result, err := forward(attemptBody)
		lastResult = result
		writeSafety.observe(c, err)

		isPrimary := candidate == requestedModel
		if isPrimary {
			triedPrimary = true
		}

		if err == nil {
			result = finalizeOpenAISameAccountFallbackSuccess(result, requestedModel, candidate)
			if stickyCtrl != nil && account != nil {
				stateCtx, stateCancel := context.WithTimeout(ctx, openAISoftMapStoreTimeout)
				switch {
				case isPrimary && plan.phase == openAISoftMapPhaseProbation:
					var firstTokenMS *int
					if result != nil {
						firstTokenMS = result.FirstTokenMs
					}
					stickyCtrl.recordPrimarySuccess(stateCtx, account.ID, requestedModel, firstTokenMS)
					stickyStateTouched = true
				case !isPrimary && isSoftCandidate(candidate) && primaryFailedWithTrigger:
					stickyCtrl.enterSticky(stateCtx, account.ID, requestedModel, candidate, softTargets, "primary_trigger_failure")
					stickyStateTouched = true
					clearStickyOnExit = false
				case !isPrimary && isSoftCandidate(candidate) && plan.phase == openAISoftMapPhaseSticky:
					// Keep sticky; if a later soft target won, update the sticky winner.
					if candidate != plan.active {
						stickyCtrl.enterSticky(stateCtx, account.ID, requestedModel, candidate, softTargets, "sticky_winner_changed")
						stickyStateTouched = true
					}
					clearStickyOnExit = false
				case isPrimary && plan.phase == "" && !primaryFailedWithTrigger:
					// Healthy primary with no sticky state: nothing to do.
				}
				stateCancel()
			}
			return result, nil
		}

		lastErr = err
		if deferred := captureOpenAIDeferredPrimaryModelUnavailable(account, requestedModel, isPrimary, err); deferred != nil {
			deferredPrimaryUnavailable = deferred
		}
		if isPrimary && isOpenAISoftMapStickyTriggerError(err) {
			primaryFailedWithTrigger = true
			if stickyCtrl != nil && account != nil && plan.phase == openAISoftMapPhaseProbation {
				stateCtx, stateCancel := context.WithTimeout(ctx, openAISoftMapStoreTimeout)
				// Keep mapped model as sticky target on probation relapse.
				active := plan.active
				if active == "" && len(softTargets) > 0 {
					active = softTargets[0]
				}
				stickyCtrl.recordPrimaryTriggerFailure(stateCtx, account.ID, requestedModel, active, softTargets, "probation_primary_failed")
				stateCancel()
				stickyStateTouched = true
				// Continue into mapped models; chain already contains them.
				plan.phase = openAISoftMapPhaseSticky
				clearStickyOnExit = true
			}
		}

		if index == len(chain)-1 {
			break
		}
		if !writeSafety.allowsModelRetry() ||
			!shouldRetryOpenAISameAccountModelFallback(ctx, settings, account, requestedModel, err) {
			break
		}
	}

	if stickyCtrl != nil && account != nil && clearStickyOnExit && !stickyStateTouched {
		// Mapped/sticky path exhausted without success: drop sticky and let account failover proceed.
		stateCtx, stateCancel := context.WithTimeout(ctx, openAISoftMapStoreTimeout)
		stickyCtrl.clearSticky(stateCtx, account.ID, requestedModel, "sticky_or_probation_exhausted")
		stateCancel()
	} else if stickyCtrl != nil && account != nil && clearStickyOnExit && primaryFailedWithTrigger && triedPrimary {
		// Probation/sticky chain ended in failure after touching state: ensure sticky is cleared
		// so the next account selection does not keep a dead mapped winner.
		stateCtx, stateCancel := context.WithTimeout(ctx, openAISoftMapStoreTimeout)
		stickyCtrl.clearSticky(stateCtx, account.ID, requestedModel, "soft_chain_failed")
		stateCancel()
	}

	// Gateway 502/503/504 deferred rate-limit handling: only penalize after the
	// same-account chain cannot recover (successful fallback must not unsched the account).
	lastErr = writeSafety.preserve(lastErr)
	deferredFlushed := false
	if deferredPrimaryUnavailable != nil {
		// Always flush the deferred primary model-unavailable, even when the chain
		// ends on a different error (429/5xx/timeout). Otherwise the account stays
		// schedulable for a model it cannot serve.
		s.recordOpenAISameAccountModelUnavailableAfterSoftChain(
			ctx,
			account,
			deferredPrimaryUnavailable.statusCode,
			deferredPrimaryUnavailable.headers,
			deferredPrimaryUnavailable.body,
			requestedModel,
		)
		deferredFlushed = true
	}
	recordOpenAISameAccountFallbackUpstreamErrorFromFailover(ctx, s, account, lastErr, requestedModel, deferredFlushed)
	return lastResult, lastErr
}

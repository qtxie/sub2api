package service

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	httppool "github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	openaipkg "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

const (
	openAIStickyFailbackProbeDefaultTimeout = 5 * time.Second
	openAIStickyFailbackProbeBodyLimit      = 64 << 10
	openAIStickyFailbackProbeModel          = "gpt-5.5"
)

var openAIStickyFailbackProbePrompts = []string{
	"What is 17 + 25? Answer with only the number.",
	"Name the capital of France in one word.",
	"Write a five-word sentence about clean code.",
	"What is the next number after 99?",
	"Translate 'good morning' to Spanish.",
}

type openAIStickyFailbackProbeResult struct {
	Healthy    bool
	StatusCode int
	Reason     string
	Err        error
	ElapsedMs  int64
}

type openAIStickyFailbackProbeCacheEntry struct {
	result    openAIStickyFailbackProbeResult
	expiresAt int64
}

var openAIStickyFailbackProbeSF singleflight.Group

func (s *OpenAIGatewayService) probeOpenAIStickyFailbackCandidate(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	account *Account,
	cfg openAIStickyPreferHigherPriorityConfig,
) openAIStickyFailbackProbeResult {
	return s.probeOpenAIStickyFailbackCandidateCached(ctx, req, account, cfg, false)
}

func (s *OpenAIGatewayService) probeOpenAIStickyFailbackCandidateFresh(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	account *Account,
	cfg openAIStickyPreferHigherPriorityConfig,
) openAIStickyFailbackProbeResult {
	return s.probeOpenAIStickyFailbackCandidateCached(ctx, req, account, cfg, true)
}

func (s *OpenAIGatewayService) probeOpenAIStickyFailbackCandidateCached(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	account *Account,
	cfg openAIStickyPreferHigherPriorityConfig,
	bypassCache bool,
) openAIStickyFailbackProbeResult {
	if !cfg.probeEnabled {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "probe_disabled"}
	}
	if s == nil || account == nil {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "invalid_account"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	cacheKey := openAIStickyFailbackProbeCacheKey(req, account)
	if !bypassCache {
		if cached, ok := s.openaiStickyFailbackProbeCache.Load(cacheKey); ok {
			if entry, ok := cached.(openAIStickyFailbackProbeCacheEntry); ok && now.UnixNano() < entry.expiresAt {
				return entry.result
			}
		}
	}

	// The singleflight group is package-wide while probe caches and transports
	// are service-instance scoped. Include the service identity so one gateway
	// instance can never execute another instance's probe closure.
	sfKey := fmt.Sprintf("%p|%s", s, cacheKey)
	if bypassCache {
		sfKey += "|fresh"
	}
	probeCtx := context.WithoutCancel(ctx)
	resultCh := openAIStickyFailbackProbeSF.DoChan(sfKey, func() (any, error) {
		if !bypassCache {
			if cached, ok := s.openaiStickyFailbackProbeCache.Load(cacheKey); ok {
				if entry, ok := cached.(openAIStickyFailbackProbeCacheEntry); ok && time.Now().UnixNano() < entry.expiresAt {
					return entry.result, nil
				}
			}
		}

		startedAt := time.Now()
		slog.Info("openai.failback_probe_started",
			"account_id", account.ID,
			"model", req.RequestedModel,
			"required_transport", req.RequiredTransport,
			"require_compact", req.RequireCompact,
		)
		result := s.runOpenAIStickyFailbackProbe(probeCtx, account, req, cfg)
		if result.ElapsedMs <= 0 {
			result.ElapsedMs = openAIStickyFailbackProbeElapsedMs(time.Since(startedAt))
		}
		slog.Info("openai.failback_probe_result",
			"account_id", account.ID,
			"healthy", result.Healthy,
			"status", result.StatusCode,
			"reason", result.Reason,
			"semantic_ttft_ms", result.ElapsedMs,
		)
		ttl := cfg.probeFailureTTL
		if result.Healthy {
			ttl = cfg.probeSuccessTTL
		}
		if ttl > 0 {
			s.openaiStickyFailbackProbeCache.Store(cacheKey, openAIStickyFailbackProbeCacheEntry{
				result:    result,
				expiresAt: time.Now().Add(ttl).UnixNano(),
			})
		} else {
			s.openaiStickyFailbackProbeCache.Delete(cacheKey)
		}
		return result, nil
	})

	var resultAny any
	select {
	case <-ctx.Done():
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "probe_caller_canceled", Err: ctx.Err()}
	case sharedResult := <-resultCh:
		if sharedResult.Err != nil {
			return openAIStickyFailbackProbeResult{Healthy: false, Reason: "probe_failed", Err: sharedResult.Err}
		}
		resultAny = sharedResult.Val
	}

	if result, ok := resultAny.(openAIStickyFailbackProbeResult); ok {
		return result
	}
	return openAIStickyFailbackProbeResult{Healthy: false, Reason: "probe_result_invalid"}
}

func (s *OpenAIGatewayService) runOpenAIStickyFailbackProbe(
	ctx context.Context,
	account *Account,
	req OpenAIAccountScheduleRequest,
	cfg openAIStickyPreferHigherPriorityConfig,
) openAIStickyFailbackProbeResult {
	if s.openaiStickyFailbackProbeRunner != nil {
		return s.openaiStickyFailbackProbeRunner(ctx, account, req, cfg)
	}
	return s.probeOpenAIStickyFailbackCandidateUpstream(ctx, account, req, cfg)
}

func (s *OpenAIGatewayService) probeOpenAIStickyFailbackCandidateUpstream(
	ctx context.Context,
	account *Account,
	req OpenAIAccountScheduleRequest,
	cfg openAIStickyPreferHigherPriorityConfig,
) (result openAIStickyFailbackProbeResult) {
	startedAt := time.Now()
	defer func() {
		if result.ElapsedMs <= 0 {
			result.ElapsedMs = openAIStickyFailbackProbeElapsedMs(time.Since(startedAt))
		}
	}()

	timeout := cfg.probeTimeout
	if timeout <= 0 {
		timeout = openAIStickyFailbackProbeDefaultTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	token, tokenType, err := s.GetAccessToken(probeCtx, account)
	if err != nil {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "token_error", Err: err}
	}

	// A compact response is unary compaction data, not model semantic output, so
	// it cannot provide a meaningful semantic TTFT. Capability compatibility is
	// checked by the scheduler; speed recovery is always proved with a streaming
	// /responses request.
	apiURL, isOAuth, err := s.openAIStickyFailbackProbeURL(account, false, tokenType)
	if err != nil {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "url_error", Err: err}
	}

	model := strings.TrimSpace(req.RequestedModel)
	if model == "" {
		model = openAIStickyFailbackProbeModel
	}
	model = account.GetMappedModel(model)

	probePrompt := openAIStickyFailbackProbePrompt()
	payload := openAIStickyFailbackProbePayload(model, isOAuth, false, probePrompt)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "payload_error", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(probeCtx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "request_error", Err: err}
	}
	httpReq = httpReq.WithContext(WithHTTPUpstreamProfile(httpReq.Context(), HTTPUpstreamProfileOpenAI))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
	httpReq.Header.Set("Originator", codexCLIOriginator)
	httpReq.Header.Set("User-Agent", codexCLIUserAgent)
	httpReq.Header.Set("Version", codexCLIVersion)
	httpReq.Header.Set("Accept", "text/event-stream")
	if isOAuth {
		httpReq.Host = "chatgpt.com"
		if err := resolveAndSetOpenAIChatGPTAccountHeaders(probeCtx, s.accountRepo, httpReq.Header, account); err != nil {
			return openAIStickyFailbackProbeResult{Healthy: false, Reason: "chatgpt_headers_error", Err: err}
		}
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.doOpenAIStickyFailbackProbeRequest(httpReq, proxyURL, account, timeout)
	if err != nil {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "request_failed", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		healthy, reason, readErr := readOpenAIStickyFailbackSemanticEvent(resp.Body)
		return openAIStickyFailbackProbeResult{Healthy: healthy, StatusCode: resp.StatusCode, Reason: reason, Err: readErr}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, openAIStickyFailbackProbeBodyLimit))
	reason := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if reason == "" {
		reason = "http_" + strconv.Itoa(resp.StatusCode)
	}
	if readErr != nil {
		return openAIStickyFailbackProbeResult{Healthy: false, StatusCode: resp.StatusCode, Reason: reason, Err: readErr}
	}
	return openAIStickyFailbackProbeResult{Healthy: false, StatusCode: resp.StatusCode, Reason: reason}
}

func readOpenAIStickyFailbackSemanticEvent(body io.Reader) (bool, string, error) {
	if body == nil {
		return false, "probe_empty_response", nil
	}
	scanner := bufio.NewScanner(io.LimitReader(body, openAIStickyFailbackProbeBodyLimit))
	scanner.Buffer(make([]byte, 4096), openAIStickyFailbackProbeBodyLimit)
	parser := openAICompatSSEFrameParser{}
	inspect := func(frame openAICompatSSEFrame, ok bool) (bool, string, bool) {
		if !ok {
			return false, "", false
		}
		payload := openAICompatPayloadWithEventType(frame.Data, frame.EventType)
		if strings.TrimSpace(payload) == "[DONE]" {
			return false, "", false
		}
		if !gjson.Valid(payload) {
			return false, "probe_malformed_event", true
		}
		eventType := strings.TrimSpace(gjson.Get(payload, "type").String())
		if eventType == "error" || eventType == "response.failed" || strings.HasSuffix(eventType, ".failed") {
			reason := strings.TrimSpace(extractOpenAISSEErrorMessage([]byte(payload)))
			if reason == "" {
				reason = "probe_error_event"
			}
			return false, reason, true
		}
		if openAIStickyFailbackProbeEventIsSemantic(payload, eventType) {
			return true, "probe_ok", true
		}
		return false, "", false
	}
	for scanner.Scan() {
		if healthy, reason, done := inspect(parser.AddLine(scanner.Text())); done {
			return healthy, reason, nil
		}
	}
	if healthy, reason, done := inspect(parser.Finish()); done {
		return healthy, reason, nil
	}
	if err := scanner.Err(); err != nil {
		return false, "probe_read_failed", err
	}
	return false, "probe_no_semantic_output", nil
}

func openAIStickyFailbackProbeEventIsSemantic(payload, eventType string) bool {
	eventType = strings.TrimSpace(eventType)
	if !openAIStreamDataStartsClientOutput(payload, eventType) {
		return false
	}
	if strings.HasSuffix(eventType, ".delta") {
		delta := gjson.Get(payload, "delta")
		return delta.Exists() && strings.TrimSpace(delta.String()) != ""
	}
	switch eventType {
	case "response.completed", "response.incomplete":
		output := gjson.Get(payload, "response.output")
		return output.Exists() && output.IsArray() && len(output.Array()) > 0
	default:
		return true
	}
}

func (s *OpenAIGatewayService) doOpenAIStickyFailbackProbeRequest(
	req *http.Request,
	proxyURL string,
	account *Account,
	timeout time.Duration,
) (*http.Response, error) {
	if s.httpUpstream != nil {
		return s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	}
	client, err := httppool.GetClient(httppool.Options{
		ProxyURL:              proxyURL,
		Timeout:               timeout,
		ResponseHeaderTimeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (s *OpenAIGatewayService) openAIStickyFailbackProbeURL(account *Account, compact bool, tokenType string) (string, bool, error) {
	if account == nil {
		return "", false, fmt.Errorf("account is nil")
	}
	isOAuth := tokenType == "oauth" || account.Type == AccountTypeOAuth
	if isOAuth {
		url := chatgptCodexURL
		if compact {
			url = appendOpenAIResponsesRequestPathSuffix(url, "/compact")
		}
		return url, true, nil
	}
	if account.Type != AccountTypeAPIKey {
		return "", false, fmt.Errorf("unsupported account type: %s", account.Type)
	}

	baseURL := account.GetOpenAIBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	normalizedBaseURL, err := s.openAIStickyFailbackValidateBaseURL(baseURL)
	if err != nil {
		return "", false, err
	}
	url := buildOpenAIResponsesURL(normalizedBaseURL)
	if compact {
		url = appendOpenAIResponsesRequestPathSuffix(url, "/compact")
	}
	return url, false, nil
}

func (s *OpenAIGatewayService) openAIStickyFailbackValidateBaseURL(baseURL string) (string, error) {
	if s != nil && s.cfg != nil {
		return s.validateUpstreamBaseURL(baseURL)
	}
	normalized, err := urlvalidator.ValidateURLFormat(baseURL, false)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}

func openAIStickyFailbackProbePayload(model string, isOAuth bool, compact bool, prompt string) map[string]any {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "What is 17 + 25? Answer with only the number."
	}
	if compact {
		return map[string]any{
			"model":        strings.TrimSpace(model),
			"instructions": "You are a helpful coding assistant.",
			"input": []any{
				map[string]any{
					"type":    "message",
					"role":    "user",
					"content": prompt,
				},
			},
		}
	}
	payload := map[string]any{
		"model": strings.TrimSpace(model),
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": prompt,
					},
				},
			},
		},
		"stream":       true,
		"instructions": openaipkg.DefaultInstructions,
	}
	if isOAuth {
		payload["store"] = false
	}
	return payload
}

func openAIStickyFailbackProbeCacheKey(req OpenAIAccountScheduleRequest, account *Account) string {
	if account == nil {
		return "invalid_account"
	}
	proxyRoute := ""
	if account.ProxyID != nil {
		proxyRoute = strconv.FormatInt(*account.ProxyID, 10)
	}
	if account.Proxy != nil {
		proxyRoute += "|" + account.Proxy.URL()
	}
	upstreamRoute := account.GetOpenAIBaseURL()
	if account.Type == AccountTypeOAuth {
		upstreamRoute = chatgptCodexURL
	}
	mappedModel := account.GetMappedModel(strings.TrimSpace(req.RequestedModel))
	routeDigest := sha256.Sum256([]byte(strings.Join([]string{
		account.Type,
		upstreamRoute,
		proxyRoute,
		mappedModel,
	}, "|")))
	parts := []string{
		strconv.FormatInt(account.ID, 10),
		strings.TrimSpace(req.RequestedModel),
		string(req.RequiredTransport),
		string(req.RequiredCapability),
		string(req.RequiredImageCapability),
		strconv.FormatBool(req.RequireCompact),
		fmt.Sprintf("%x", routeDigest[:12]),
	}
	return strings.Join(parts, "|")
}

func openAIStickyFailbackProbePrompt() string {
	return openAIStickyFailbackProbePrompts[openAIStickyFailbackProbeRandomIndex(len(openAIStickyFailbackProbePrompts))]
}

func openAIStickyFailbackProbeElapsedMs(elapsed time.Duration) int64 {
	if elapsed < 0 {
		return 0
	}
	if elapsed < time.Millisecond {
		return 1
	}
	return elapsed.Milliseconds()
}

func openAIStickyFailbackProbeRandomIndex(length int) int {
	if length <= 1 {
		return 0
	}
	var seed [8]byte
	if _, err := cryptorand.Read(seed[:]); err == nil {
		return int(binary.LittleEndian.Uint64(seed[:]) % uint64(length))
	}
	return int(time.Now().UnixNano() % int64(length))
}

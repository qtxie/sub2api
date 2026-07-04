package service

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	httppool "github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	openaipkg "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"golang.org/x/sync/singleflight"
)

const (
	openAIStickyFailbackProbeDefaultTimeout = 5 * time.Second
	openAIStickyFailbackProbeBodyLimit      = 64 << 10
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
		return openAIStickyFailbackProbeResult{Healthy: true, Reason: "probe_disabled"}
	}
	if s == nil || account == nil {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "invalid_account"}
	}
	if req.RequiredImageCapability != "" {
		return openAIStickyFailbackProbeResult{Healthy: true, Reason: "image_probe_skipped"}
	}

	now := time.Now()
	cacheKey := openAIStickyFailbackProbeCacheKey(req, account.ID)
	if !bypassCache {
		if cached, ok := s.openaiStickyFailbackProbeCache.Load(cacheKey); ok {
			if entry, ok := cached.(openAIStickyFailbackProbeCacheEntry); ok && now.UnixNano() < entry.expiresAt {
				return entry.result
			}
		}
	}

	sfKey := cacheKey
	if bypassCache {
		sfKey += "|fresh"
	}
	resultAny, _, _ := openAIStickyFailbackProbeSF.Do(sfKey, func() (any, error) {
		if !bypassCache {
			if cached, ok := s.openaiStickyFailbackProbeCache.Load(cacheKey); ok {
				if entry, ok := cached.(openAIStickyFailbackProbeCacheEntry); ok && time.Now().UnixNano() < entry.expiresAt {
					return entry.result, nil
				}
			}
		}

		result := s.runOpenAIStickyFailbackProbe(ctx, account, req, cfg)
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
) openAIStickyFailbackProbeResult {
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

	apiURL, isOAuth, err := s.openAIStickyFailbackProbeURL(account, req.RequireCompact, tokenType)
	if err != nil {
		return openAIStickyFailbackProbeResult{Healthy: false, Reason: "url_error", Err: err}
	}

	model := strings.TrimSpace(req.RequestedModel)
	if model == "" {
		model = openaipkg.DefaultTestModel
	}
	model = account.GetMappedModel(model)
	if req.RequireCompact {
		model = resolveOpenAICompactForwardModel(account, model)
	}

	probePrompt := openAIStickyFailbackProbePrompt()
	payload := openAIStickyFailbackProbePayload(model, isOAuth, req.RequireCompact, probePrompt)
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
	httpReq.Header.Set("Originator", "codex_cli_rs")
	httpReq.Header.Set("User-Agent", codexCLIUserAgent)
	httpReq.Header.Set("Version", codexCLIVersion)
	if req.RequireCompact {
		httpReq.Header.Set("Accept", "application/json")
		probeSessionID := compactProbeSessionID(account.ID)
		httpReq.Header.Set("Session_ID", probeSessionID)
		httpReq.Header.Set("Conversation_ID", probeSessionID)
	} else {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
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

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, openAIStickyFailbackProbeBodyLimit))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return openAIStickyFailbackProbeResult{Healthy: true, StatusCode: resp.StatusCode, Reason: "probe_ok"}
	}
	reason := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if reason == "" {
		reason = "http_" + strconv.Itoa(resp.StatusCode)
	}
	if readErr != nil {
		return openAIStickyFailbackProbeResult{Healthy: false, StatusCode: resp.StatusCode, Reason: reason, Err: readErr}
	}
	return openAIStickyFailbackProbeResult{Healthy: false, StatusCode: resp.StatusCode, Reason: reason}
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

func openAIStickyFailbackProbeCacheKey(req OpenAIAccountScheduleRequest, accountID int64) string {
	parts := []string{
		strconv.FormatInt(accountID, 10),
		strings.TrimSpace(req.RequestedModel),
		string(req.RequiredTransport),
		string(req.RequiredCapability),
		string(req.RequiredImageCapability),
		strconv.FormatBool(req.RequireCompact),
	}
	return strings.Join(parts, "|")
}

func openAIStickyFailbackProbePrompt() string {
	return openAIStickyFailbackProbePrompts[openAIStickyFailbackProbeRandomIndex(len(openAIStickyFailbackProbePrompts))]
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

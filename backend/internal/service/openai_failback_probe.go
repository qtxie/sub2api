package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/tidwall/gjson"
)

const (
	openAIFailbackProbeMaxOutputTokens = 128
	openAIFailbackProbeMaxEventBytes   = 1 << 20
	openAIFailbackProbeMaxErrorBytes   = 8 << 10
)

type openAIFailbackProbeResult struct {
	TTFTMS int
}

func (s *OpenAIGatewayService) runOpenAIFailbackProbe(
	ctx context.Context,
	account *Account,
	mappedModel string,
	requiredCapability OpenAIEndpointCapability,
) (openAIFailbackProbeResult, error) {
	if s == nil || s.httpUpstream == nil {
		return openAIFailbackProbeResult{}, errors.New("OpenAI failback probe upstream is unavailable")
	}
	if account == nil || account.ID <= 0 || account.Platform != PlatformOpenAI {
		return openAIFailbackProbeResult{}, errors.New("OpenAI failback probe requires an OpenAI account")
	}
	mappedModel = strings.TrimSpace(mappedModel)
	if mappedModel == "" {
		return openAIFailbackProbeResult{}, errors.New("OpenAI failback probe model is empty")
	}

	req, proxyURL, err := s.buildOpenAIFailbackProbeRequest(ctx, account, mappedModel, requiredCapability)
	if err != nil {
		return openAIFailbackProbeResult{}, err
	}
	startedAt := time.Now()
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return openAIFailbackProbeResult{}, err
	}
	if resp == nil {
		return openAIFailbackProbeResult{}, errors.New("OpenAI failback probe returned no response")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, openAIFailbackProbeMaxErrorBytes))
		message := strings.TrimSpace(extractUpstreamErrorMessage(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return openAIFailbackProbeResult{}, fmt.Errorf("OpenAI failback probe returned HTTP %d: %s", resp.StatusCode, message)
	}

	if err := scanOpenAIFailbackProbeOutput(resp.Body); err != nil {
		return openAIFailbackProbeResult{}, err
	}
	ttft := int(time.Since(startedAt).Milliseconds())
	if ttft <= 0 {
		ttft = 1
	}
	return openAIFailbackProbeResult{TTFTMS: ttft}, nil
}

func (s *OpenAIGatewayService) buildOpenAIFailbackProbeRequest(
	ctx context.Context,
	account *Account,
	mappedModel string,
	requiredCapability OpenAIEndpointCapability,
) (*http.Request, string, error) {
	credentialAccount := account
	if account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, "", err
		}
		credentialAccount = resolved
	}

	useResponses := credentialAccount.IsOAuth() || openai_compat.ShouldUseResponsesAPI(account.Extra)
	if requiredCapability == OpenAIEndpointCapabilityChatCompletions && credentialAccount.Type == AccountTypeAPIKey {
		useResponses = false
	}
	if requiredCapability == OpenAIEndpointCapabilityResponses {
		useResponses = true
	}

	upstreamModel := normalizeOpenAIModelForUpstream(credentialAccount, mappedModel)
	var targetURL string
	var payload map[string]any
	if useResponses {
		targetURL = openaiPlatformAPIURL
		if credentialAccount.IsOAuth() {
			targetURL = chatgptCodexAPIURL
		} else if baseURL := strings.TrimSpace(account.GetOpenAIBaseURL()); baseURL != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, "", err
			}
			targetURL = buildOpenAIResponsesURL(validatedURL)
		}
		payload = map[string]any{
			"model":             upstreamModel,
			"instructions":      "Reply only OK.",
			"input":             "Reply only OK.",
			"max_output_tokens": openAIFailbackProbeMaxOutputTokens,
			"stream":            true,
			"store":             false,
		}
	} else {
		baseURL := strings.TrimSpace(account.GetOpenAIBaseURL())
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		validatedURL, err := s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return nil, "", err
		}
		targetURL = buildOpenAIChatCompletionsURL(validatedURL)
		payload = map[string]any{
			"model": upstreamModel,
			"messages": []map[string]any{{
				"role":    "user",
				"content": "Reply only OK.",
			}},
			"max_tokens": openAIFailbackProbeMaxOutputTokens,
			"stream":     true,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, "", err
	}
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, "", err
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if credentialAccount.IsOAuth() {
		req.Host = "chatgpt.com"
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("Originator", "codex_cli_rs")
		if customUA := strings.TrimSpace(credentialAccount.GetOpenAIUserAgent()); customUA != "" {
			req.Header.Set("User-Agent", customUA)
		} else {
			req.Header.Set("User-Agent", codexCLIUserAgent)
		}
		setOpenAIChatGPTAccountHeaders(req.Header, credentialAccount)
		enforceCodexIdentityHeaders(req.Header)
	} else if customUA := strings.TrimSpace(account.GetOpenAIUserAgent()); customUA != "" {
		req.Header.Set("User-Agent", customUA)
	}
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return req, proxyURL, nil
}

func scanOpenAIFailbackProbeOutput(body io.Reader) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), openAIFailbackProbeMaxEventBytes)
	eventType := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			eventType = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		data := line
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		} else if !gjson.Valid(line) {
			continue
		}
		if data == "" || data == "[DONE]" {
			continue
		}
		payloadType := strings.TrimSpace(gjson.Get(data, "type").String())
		if payloadType != "" {
			eventType = payloadType
		}
		if eventType == "response.failed" || eventType == "error" || gjson.Get(data, "error").Exists() {
			return fmt.Errorf("OpenAI failback probe stream failed: %s", truncateString(data, 512))
		}
		if openAIFailbackProbeHasSemanticOutput(data, eventType) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("OpenAI failback probe ended before semantic output")
}

func openAIFailbackProbeHasSemanticOutput(data, eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.output_text.delta", "response.refusal.delta":
		return strings.TrimSpace(gjson.Get(data, "delta").String()) != ""
	}
	for _, path := range []string{
		"choices.0.delta.content",
		"choices.0.delta.reasoning_content",
		"choices.0.message.content",
		"response.output.0.content.0.text",
		"output.0.content.0.text",
		"output_text",
	} {
		if strings.TrimSpace(gjson.Get(data, path).String()) != "" {
			return true
		}
	}
	return false
}

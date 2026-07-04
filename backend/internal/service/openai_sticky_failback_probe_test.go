package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/tidwall/gjson"
)

type openAIStickyFailbackProbeHTTPUpstreamStub struct {
	req *http.Request
}

func (s *openAIStickyFailbackProbeHTTPUpstreamStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	s.req = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("data: {}\n\n")),
	}, nil
}

func (s *openAIStickyFailbackProbeHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestOpenAIStickyFailbackProbeUsesCodexResponsesPayloadShape(t *testing.T) {
	upstream := &openAIStickyFailbackProbeHTTPUpstreamStub{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          501,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result := svc.probeOpenAIStickyFailbackCandidateUpstream(context.Background(), account, OpenAIAccountScheduleRequest{
		RequestedModel: "gpt-5.1",
	}, openAIStickyPreferHigherPriorityConfig{probeTimeout: time.Second})
	if !result.Healthy {
		t.Fatalf("probe healthy=%v reason=%s err=%v", result.Healthy, result.Reason, result.Err)
	}
	if upstream.req == nil {
		t.Fatal("expected probe request")
	}
	if got := upstream.req.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept=%q want text/event-stream", got)
	}
	body, err := io.ReadAll(upstream.req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if got := gjson.GetBytes(body, "stream"); !got.Bool() {
		t.Fatalf("stream=%s want true", got.Raw)
	}
	if gjson.GetBytes(body, "max_output_tokens").Exists() {
		t.Fatal("probe payload should not set max_output_tokens")
	}
	if got := gjson.GetBytes(body, "instructions"); strings.TrimSpace(got.String()) == "" {
		t.Fatal("probe payload should include instructions")
	}
	prompt := strings.TrimSpace(gjson.GetBytes(body, "input.0.content.0.text").String())
	if !isKnownOpenAIStickyFailbackProbePrompt(prompt) {
		t.Fatalf("unexpected probe prompt %q", prompt)
	}
	if ua := upstream.req.Header.Get("User-Agent"); ua != codexCLIUserAgent {
		t.Fatalf("User-Agent=%q want %q", ua, codexCLIUserAgent)
	}
	if originator := upstream.req.Header.Get("Originator"); originator != "codex_cli_rs" {
		t.Fatalf("Originator=%q want codex_cli_rs", originator)
	}
}

func TestOpenAIStickyFailbackProbePayloadUsesMeaningfulPrompt(t *testing.T) {
	payload := openAIStickyFailbackProbePayload("gpt-5.1", false, false, "What is 17 + 25?")
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if got := strings.TrimSpace(gjson.GetBytes(body, "input.0.content.0.text").String()); got != "What is 17 + 25?" {
		t.Fatalf("prompt=%q", got)
	}
}

func isKnownOpenAIStickyFailbackProbePrompt(prompt string) bool {
	for _, candidate := range openAIStickyFailbackProbePrompts {
		if prompt == candidate {
			return true
		}
	}
	return false
}

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/tidwall/gjson"
)

func TestReadOpenAIStickyFailbackSemanticEvent(t *testing.T) {
	tests := []struct {
		name       string
		stream     string
		healthy    bool
		wantReason string
	}{
		{
			name: "preamble and empty completion",
			stream: ": heartbeat\n\n" +
				"data: {\"type\":\"response.created\"}\n\n" +
				"data: {\"type\":\"response.in_progress\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n",
			wantReason: "probe_no_semantic_output",
		},
		{
			name:       "nonempty output delta",
			stream:     "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"42\"}\n\n",
			healthy:    true,
			wantReason: "probe_ok",
		},
		{
			name:       "completed response with output",
			stream:     "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\"}]}}\n\n",
			healthy:    true,
			wantReason: "probe_ok",
		},
		{
			name:       "failed response",
			stream:     "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"overloaded\"}}}\n\n",
			wantReason: "overloaded",
		},
		{
			name:       "bare error event",
			stream:     "event: error\ndata: {\"error\":{\"message\":\"upstream unavailable\"}}\n\n",
			wantReason: "upstream unavailable",
		},
		{
			name:       "malformed event",
			stream:     "data: {not-json}\n\n",
			wantReason: "probe_malformed_event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			healthy, reason, err := readOpenAIStickyFailbackSemanticEvent(strings.NewReader(tt.stream))
			if err != nil {
				t.Fatalf("read semantic event: %v", err)
			}
			if healthy != tt.healthy || reason != tt.wantReason {
				t.Fatalf("healthy=%v reason=%q want healthy=%v reason=%q", healthy, reason, tt.healthy, tt.wantReason)
			}
		})
	}
}

type openAIStickyFailbackProbeHTTPUpstreamStub struct {
	req *http.Request
}

func (s *openAIStickyFailbackProbeHTTPUpstreamStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	s.req = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"42\"}\n\n")),
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
	if result.ElapsedMs <= 0 {
		t.Fatalf("probe ElapsedMs=%d want positive", result.ElapsedMs)
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
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.1" {
		t.Fatalf("model=%q want %q", got, "gpt-5.1")
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
	if originator := upstream.req.Header.Get("Originator"); originator != codexCLIOriginator {
		t.Fatalf("Originator=%q want %q", originator, codexCLIOriginator)
	}
}

func TestOpenAIStickyFailbackCompactCandidateStillUsesSemanticResponsesProbe(t *testing.T) {
	upstream := &openAIStickyFailbackProbeHTTPUpstreamStub{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          502,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result := svc.probeOpenAIStickyFailbackCandidateUpstream(context.Background(), account, OpenAIAccountScheduleRequest{
		RequestedModel: "gpt-5.1",
		RequireCompact: true,
	}, openAIStickyPreferHigherPriorityConfig{probeTimeout: time.Second})

	if !result.Healthy {
		t.Fatalf("probe healthy=%v reason=%s err=%v", result.Healthy, result.Reason, result.Err)
	}
	if upstream.req == nil || strings.HasSuffix(upstream.req.URL.Path, "/compact") {
		t.Fatalf("probe path=%v, want semantic /responses endpoint", upstream.req)
	}
	if got := upstream.req.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept=%q want text/event-stream", got)
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

func TestOpenAIStickyFailbackProbeDisabledIsNotHealthy(t *testing.T) {
	svc := &OpenAIGatewayService{}
	result := svc.probeOpenAIStickyFailbackCandidate(context.Background(), OpenAIAccountScheduleRequest{}, &Account{ID: 90001}, openAIStickyPreferHigherPriorityConfig{})

	if result.Healthy || result.Reason != "probe_disabled" {
		t.Fatalf("healthy=%v reason=%q, want disabled probe to remain unverified", result.Healthy, result.Reason)
	}
}

func TestOpenAIStickyFailbackProbeCallerCancellationDoesNotPoisonSharedProbe(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	svc := &OpenAIGatewayService{}
	svc.openaiStickyFailbackProbeRunner = func(ctx context.Context, _ *Account, _ OpenAIAccountScheduleRequest, _ openAIStickyPreferHigherPriorityConfig) openAIStickyFailbackProbeResult {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return openAIStickyFailbackProbeResult{Healthy: true, Reason: "probe_ok", ElapsedMs: 50}
		case <-ctx.Done():
			return openAIStickyFailbackProbeResult{Healthy: false, Reason: "probe_context_canceled", Err: ctx.Err()}
		}
	}
	cfg := openAIStickyPreferHigherPriorityConfig{
		probeEnabled:    true,
		probeSuccessTTL: time.Second,
		probeFailureTTL: time.Second,
	}
	req := OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.1"}
	account := &Account{ID: 90002}

	callerCtx, cancel := context.WithCancel(context.Background())
	firstResultCh := make(chan openAIStickyFailbackProbeResult, 1)
	go func() {
		firstResultCh <- svc.probeOpenAIStickyFailbackCandidate(callerCtx, req, account, cfg)
	}()
	<-started
	secondResultCh := make(chan openAIStickyFailbackProbeResult, 1)
	go func() {
		secondResultCh <- svc.probeOpenAIStickyFailbackCandidate(context.Background(), req, account, cfg)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	first := <-firstResultCh
	if first.Healthy || first.Reason != "probe_caller_canceled" {
		t.Fatalf("first result healthy=%v reason=%q", first.Healthy, first.Reason)
	}
	close(release)
	second := <-secondResultCh
	if !second.Healthy || second.Reason != "probe_ok" {
		t.Fatalf("second result healthy=%v reason=%q err=%v", second.Healthy, second.Reason, second.Err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe calls=%d, want one shared probe", got)
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

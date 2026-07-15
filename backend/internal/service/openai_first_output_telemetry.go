package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

func logOpenAIFirstOutputResponseHeaders(ctx context.Context, account *Account, resp *http.Response, latency time.Duration) {
	attempt := OpenAIFirstOutputAttemptFromContext(ctx)
	if attempt.Attempt <= 0 || account == nil || resp == nil {
		return
	}
	fields := openAIFirstOutputAttemptLogFields(attempt, account)
	fields = append(fields,
		zap.Int("upstream_status", resp.StatusCode),
		zap.String("upstream_protocol", resp.Proto),
		zap.String("upstream_request_id", strings.TrimSpace(resp.Header.Get("x-request-id"))),
		zap.Int64("response_header_latency_ms", latency.Milliseconds()),
	)
	logger.FromContext(ctx).Info("openai.first_output_attempt_headers", fields...)
}

func logOpenAIFirstOutputRawEvent(ctx context.Context, account *Account, eventType string, cLatencyAttempt, cLatencyTotal int64) {
	attempt := OpenAIFirstOutputAttemptFromContext(ctx)
	if attempt.Attempt <= 0 || account == nil {
		return
	}
	fields := openAIFirstOutputAttemptLogFields(attempt, account)
	fields = append(fields,
		zap.String("event_type", strings.TrimSpace(eventType)),
		zap.Int64("attempt_latency_ms", cLatencyAttempt),
		zap.Int64("total_pre_output_latency_ms", cLatencyTotal),
	)
	logger.FromContext(ctx).Info("openai.first_output_attempt_raw_sse", fields...)
}

func logOpenAIFirstOutputSemantic(ctx context.Context, account *Account, eventType string, attemptMs, totalMs int) {
	attempt := OpenAIFirstOutputAttemptFromContext(ctx)
	if attempt.Attempt <= 0 || account == nil {
		return
	}
	fields := openAIFirstOutputAttemptLogFields(attempt, account)
	fields = append(fields,
		zap.String("event_type", strings.TrimSpace(eventType)),
		zap.Int("attempt_latency_ms", attemptMs),
		zap.Int("total_pre_output_latency_ms", totalMs),
	)
	logger.FromContext(ctx).Info("openai.first_output_attempt_semantic_sse", fields...)
}

func openAIFirstOutputAttemptLogFields(attempt OpenAIFirstOutputAttempt, account *Account) []zap.Field {
	return []zap.Field{
		zap.Int64("account_id", account.ID),
		zap.Int("attempt", attempt.Attempt),
		zap.Int("same_account_retry", attempt.Retry),
		zap.String("route", attempt.Route),
		zap.Int64("proxy_id", attempt.ProxyID),
		zap.String("proxy_name", attempt.ProxyName),
		zap.Bool("fresh_transport", attempt.FreshTransport),
	}
}

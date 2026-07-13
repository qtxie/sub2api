package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// forwardResponsesViaRawChatCompletions serves /v1/responses clients through an
// upstream that only supports /v1/chat/completions.
func (s *OpenAIGatewayService) forwardResponsesViaRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := strings.TrimSpace(responsesReq.Model)
	if originalModel == "" {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}

	clientStream := responsesReq.Stream
	serviceTier := extractOpenAIServiceTierFromBody(body)
	// custom 工具（如 codex 的 exec）降级为 function 工具转发，回程需按名字还原为
	// custom_tool_call 项，先记下名字集合；tool_search 工具同理，回程还原为
	// tool_search_call 项；namespace 子工具（如 MCP 工具）摊平转发，回程按映射还原
	// 为带 namespace 字段的 function_call 项。
	effectiveTools, err := apicompat.EffectiveResponsesTools(&responsesReq)
	if err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("resolve responses tools: %w", err)
	}
	customTools := apicompat.CustomToolNames(effectiveTools)
	toolSearch := apicompat.HasToolSearchTool(effectiveTools)
	namespaceTools := apicompat.NamespaceToolNames(effectiveTools)

	chatReq, err := apicompat.ResponsesToChatCompletionsRequest(&responsesReq)
	if err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert responses to chat completions: %w", err)
	}

	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	reasoningEffort := extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	// 国产模型默认 effort 补充：需要 mappedModel 判定，推迟到 billingModel 算出之后。
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, billingModel)
	chatReq.Model = upstreamModel
	if clientStream {
		chatReq.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
	}

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions fallback request: %w", err)
	}
	chatBody, err = s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, chatBody)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, err
	}
	if serviceTier == nil {
		serviceTier = extractOpenAIServiceTierFromBody(chatBody)
	}

	logger.L().Debug("openai responses: forwarding via raw chat completions",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	// Build and send upstream request via the shared CC pipeline
	apiKey, targetURL, err := s.resolveCCFallbackTarget(account)
	if err != nil {
		return nil, err
	}
	resp, err := s.sendCCUpstreamRequest(ctx, c, account, targetURL, chatBody, clientStream, apiKey, resolveOpenAIUpstreamUserAgent(account, c.GetHeader("User-Agent")), "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return s.handleErrorResponse(ctx, resp, c, account, chatBody, billingModel)
	}

	if clientStream {
		return s.streamChatCompletionsAsResponses(ctx, c, resp, originalModel, customTools, toolSearch, namespaceTools, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	return s.bufferChatCompletionsAsResponses(c, resp, originalModel, customTools, toolSearch, namespaceTools, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
}

func (s *OpenAIGatewayService) bufferChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	customTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	ccResp, usage, err := s.readCCUpstreamJSONResponse(c, resp, writeOpenAIResponsesFallbackError)
	if err != nil {
		return nil, err
	}
	responsesResp := apicompat.ChatCompletionsResponseToResponses(ccResp, originalModel, customTools, toolSearch, namespaceTools)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.JSON(http.StatusOK, responsesResp)

	return &OpenAIForwardResult{
		RequestID:       requestID,
		Usage:           usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) streamChatCompletionsAsResponses(
	ctx context.Context,
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	customTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)

	state := apicompat.NewChatCompletionsToResponsesStreamState(originalModel)
	state.CustomTools = customTools
	state.ToolSearchDeclared = toolSearch
	state.NamespaceTools = namespaceTools
	clientDisconnected := false
	var preambleBuffer []byte
	var pendingLines []string
	var semanticFirstTokenMs *int
	var attemptFirstTokenMs *int
	var streamEarlyErr error
	clientOutputStarted := false
	bufferedWriter := bufio.NewWriterSize(c.Writer, 4*1024)
	writeBuffered := func(write func() error, flush bool) error {
		return OpenAIPreOutputWithWriterLock(c, func() error {
			if write != nil {
				if err := write(); err != nil {
					return err
				}
			}
			if !flush {
				return nil
			}
			if err := bufferedWriter.Flush(); err != nil {
				return err
			}
			c.Writer.Flush()
			return nil
		})
	}
	writeBufferedAndMarkSemantic := func(write func() error) (totalMs, attemptMs int, transitioned bool, err error) {
		commit := func() error {
			if write != nil {
				if err := write(); err != nil {
					return err
				}
			}
			if err := bufferedWriter.Flush(); err != nil {
				return err
			}
			c.Writer.Flush()
			return nil
		}
		if OpenAIPreOutputEnabled(c) {
			return OpenAIPreOutputCommitSemantic(c, ctx, commit)
		}
		err = commit()
		if err == nil {
			totalMs = int(time.Since(startTime).Milliseconds())
			attemptMs = totalMs
			transitioned = true
		}
		return
	}
	markClientDisconnected := func() {
		if clientDisconnected {
			return
		}
		clientDisconnected = true
		OpenAIPreOutputMarkClientDisconnected(c)
	}

	writeEvents := func(events []apicompat.ResponsesStreamEvent) {
		if clientDisconnected || len(events) == 0 || streamEarlyErr != nil {
			return
		}
		for _, event := range events {
			sse, err := apicompat.ResponsesEventToSSE(event)
			if err != nil {
				logger.L().Warn("openai responses chat fallback: failed to marshal stream event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}
			startsClientOutput := !openAIStreamEventIsPreamble(event.Type)
			coordinatedPreOutput := OpenAIPreOutputEnabled(c) && !OpenAIPreOutputSemanticStarted(c)
			if OpenAIPreOutputEnabled(c) && !OpenAIPreOutputClientConnected(c) {
				markClientDisconnected()
			}
			if coordinatedPreOutput && !startsClientOutput {
				preambleBuffer, err = appendOpenAIPreOutputPreamble(preambleBuffer, strings.TrimSuffix(sse, "\n"))
				if err != nil {
					streamEarlyErr = s.newOpenAIStreamFailoverError(c, nil, false, requestID, nil, "OpenAI stream preamble exceeded limit before semantic output")
					_ = resp.Body.Close()
					return
				}
				continue
			}
			if !OpenAIPreOutputEnabled(c) && !clientOutputStarted && !startsClientOutput {
				pendingLines = append(pendingLines, sse)
				continue
			}
			writeStreamHeaders()
			if coordinatedPreOutput && startsClientOutput {
				commitPreamble := preambleBuffer
				preambleBuffer = nil
				semanticTotalMs, semanticAttemptMs, semanticTransitioned, flushErr := writeBufferedAndMarkSemantic(func() error {
					if len(commitPreamble) > 0 {
						if _, err := bufferedWriter.Write(commitPreamble); err != nil {
							return err
						}
					}
					_, err := bufferedWriter.WriteString(sse)
					return err
				})
				if flushErr != nil {
					if IsOpenAIPreOutputFailure(flushErr) {
						streamEarlyErr = flushErr
						_ = resp.Body.Close()
						return
					}
					markClientDisconnected()
					return
				}
				if semanticTransitioned {
					ms := semanticTotalMs
					semanticFirstTokenMs = &ms
					attempt := semanticAttemptMs
					attemptFirstTokenMs = &attempt
				}
				clientOutputStarted = true
				continue
			}
			pending := pendingLines
			pendingLines = nil
			writeEvent := func() error {
				for _, pendingEvent := range pending {
					if _, err := bufferedWriter.WriteString(pendingEvent); err != nil {
						return err
					}
					if _, err := bufferedWriter.WriteString("\n"); err != nil {
						return err
					}
				}
				_, err := bufferedWriter.WriteString(sse)
				return err
			}
			var flushErr error
			var semanticTotalMs, semanticAttemptMs int
			var semanticTransitioned bool
			if startsClientOutput && semanticFirstTokenMs == nil {
				semanticTotalMs, semanticAttemptMs, semanticTransitioned, flushErr = writeBufferedAndMarkSemantic(writeEvent)
			} else {
				flushErr = writeBuffered(writeEvent, true)
			}
			if flushErr != nil {
				if IsOpenAIPreOutputFailure(flushErr) {
					streamEarlyErr = flushErr
					_ = resp.Body.Close()
					return
				}
				markClientDisconnected()
				logger.L().Debug("openai responses chat fallback: client disconnected, continuing to drain upstream for billing",
					zap.Error(flushErr),
					zap.String("request_id", requestID),
				)
				return
			}
			if startsClientOutput && semanticFirstTokenMs == nil && semanticTransitioned {
				ms := semanticTotalMs
				semanticFirstTokenMs = &ms
				attempt := semanticAttemptMs
				attemptFirstTokenMs = &attempt
			}
		}
	}

	scan := s.scanCCStream(resp, "openai responses chat fallback", requestID, startTime, func(chunk *apicompat.ChatCompletionsChunk) {
		writeEvents(apicompat.ChatCompletionsChunkToResponsesEvents(chunk, state))
	})
	if scan.Err == nil && OpenAIPreOutputEnabled(c) {
		scan.Err = ctx.Err()
	}
	if streamEarlyErr != nil {
		return &OpenAIForwardResult{
			RequestID:           requestID,
			Usage:               scan.Usage,
			Model:               originalModel,
			BillingModel:        billingModel,
			UpstreamModel:       upstreamModel,
			ReasoningEffort:     reasoningEffort,
			ServiceTier:         serviceTier,
			Stream:              true,
			Duration:            time.Since(startTime),
			FirstTokenMs:        semanticFirstTokenMs,
			AttemptFirstTokenMs: attemptFirstTokenMs,
		}, streamEarlyErr
	}

	if scan.Err != nil {
		if timeoutErr := OpenAIPreOutputFailureError(c, ctx, scan.Err); IsOpenAIPreOutputFailure(timeoutErr) {
			return &OpenAIForwardResult{
				RequestID:           requestID,
				Usage:               scan.Usage,
				Model:               originalModel,
				BillingModel:        billingModel,
				UpstreamModel:       upstreamModel,
				ReasoningEffort:     reasoningEffort,
				ServiceTier:         serviceTier,
				Stream:              true,
				Duration:            time.Since(startTime),
				FirstTokenMs:        semanticFirstTokenMs,
				AttemptFirstTokenMs: attemptFirstTokenMs,
			}, timeoutErr
		}
		firstTokenMs := scan.FirstTokenMs
		if semanticFirstTokenMs != nil {
			firstTokenMs = semanticFirstTokenMs
		}
		attemptTTFT := attemptFirstTokenMs
		if attemptTTFT == nil {
			attemptTTFT = scan.FirstTokenMs
		}
		return &OpenAIForwardResult{
			RequestID:           requestID,
			Usage:               scan.Usage,
			Model:               originalModel,
			BillingModel:        billingModel,
			UpstreamModel:       upstreamModel,
			ReasoningEffort:     reasoningEffort,
			ServiceTier:         serviceTier,
			Stream:              true,
			Duration:            time.Since(startTime),
			FirstTokenMs:        firstTokenMs,
			AttemptFirstTokenMs: attemptTTFT,
		}, fmt.Errorf("stream usage incomplete: %w", scan.Err)
	}

	writeEvents(apicompat.FinalizeChatCompletionsResponsesStream(state))
	if streamEarlyErr != nil {
		return &OpenAIForwardResult{
			RequestID: requestID, Usage: scan.Usage, Model: originalModel, BillingModel: billingModel,
			UpstreamModel: upstreamModel, ReasoningEffort: reasoningEffort, ServiceTier: serviceTier,
			Stream: true, Duration: time.Since(startTime), FirstTokenMs: semanticFirstTokenMs,
			AttemptFirstTokenMs: attemptFirstTokenMs,
		}, streamEarlyErr
	}
	if OpenAIPreOutputEnabled(c) && !OpenAIPreOutputSemanticStarted(c) && !clientDisconnected {
		return &OpenAIForwardResult{
			RequestID: requestID, Usage: scan.Usage, Model: originalModel, BillingModel: billingModel,
			UpstreamModel: upstreamModel, ReasoningEffort: reasoningEffort, ServiceTier: serviceTier,
			Stream: true, Duration: time.Since(startTime), FirstTokenMs: semanticFirstTokenMs,
			AttemptFirstTokenMs: attemptFirstTokenMs,
		}, s.newOpenAIStreamFailoverError(c, nil, false, requestID, nil, "OpenAI compatibility stream ended before semantic output")
	}
	if !clientDisconnected {
		writeStreamHeaders()
		pending := pendingLines
		pendingLines = nil
		if err := writeBuffered(func() error {
			for _, pendingEvent := range pending {
				if _, err := bufferedWriter.WriteString(pendingEvent); err != nil {
					return err
				}
				if _, err := bufferedWriter.WriteString("\n"); err != nil {
					return err
				}
			}
			_, err := bufferedWriter.WriteString("data: [DONE]\n\n")
			return err
		}, true); err != nil {
			markClientDisconnected()
		}
	}
	if !scan.SawDone {
		logCCStreamMissingDoneSentinel("openai responses chat fallback", requestID)
	}
	firstTokenMs := scan.FirstTokenMs
	if semanticFirstTokenMs != nil {
		firstTokenMs = semanticFirstTokenMs
	}
	if attemptFirstTokenMs == nil {
		attemptFirstTokenMs = scan.FirstTokenMs
	}

	return &OpenAIForwardResult{
		RequestID:           requestID,
		Usage:               scan.Usage,
		Model:               originalModel,
		BillingModel:        billingModel,
		UpstreamModel:       upstreamModel,
		ReasoningEffort:     reasoningEffort,
		ServiceTier:         serviceTier,
		Stream:              true,
		Duration:            time.Since(startTime),
		FirstTokenMs:        firstTokenMs,
		AttemptFirstTokenMs: attemptFirstTokenMs,
	}, nil
}

func chatChunkStartsResponsesOutput(chunk *apicompat.ChatCompletionsChunk) bool {
	if chunk == nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil || choice.Delta.ReasoningContent != nil || len(choice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

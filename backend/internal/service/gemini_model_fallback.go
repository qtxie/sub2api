package service

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type geminiBodyForward func(body []byte) (*ForwardResult, error)

func (s *GeminiMessagesCompatService) Forward(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	return s.forwardBodyWithSameAccountModelFallback(ctx, account, body, func(attemptBody []byte) (*ForwardResult, error) {
		return s.forwardOnce(ctx, c, account, attemptBody)
	})
}

func (s *GeminiMessagesCompatService) ForwardAsChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	return s.forwardBodyWithSameAccountModelFallback(ctx, account, body, func(attemptBody []byte) (*ForwardResult, error) {
		return s.forwardAsChatCompletionsOnce(ctx, c, account, attemptBody)
	})
}

func (s *GeminiMessagesCompatService) forwardBodyWithSameAccountModelFallback(
	ctx context.Context,
	account *Account,
	body []byte,
	forward geminiBodyForward,
) (*ForwardResult, error) {
	requestedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	result, err := forward(body)
	if err == nil || requestedModel == "" || account == nil || !IsModelUnavailableFailover(err) {
		return result, err
	}

	chain := []string{requestedModel}
	if s != nil && s.settingService != nil {
		chain = s.settingService.BuildModelFallbackChain(ctx, PlatformGemini, requestedModel)
	}
	lastErr := err
	for _, candidate := range chain[1:] {
		result, err = forward(ReplaceModelInBody(body, candidate))
		if err == nil {
			if result != nil {
				result.Model = requestedModel
			}
			return result, nil
		}
		lastErr = err
		if !IsModelUnavailableFailover(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (s *GeminiMessagesCompatService) ForwardNative(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	requestedModel string,
	action string,
	stream bool,
	body []byte,
) (*ForwardResult, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	result, err := s.forwardNativeOnce(ctx, c, account, requestedModel, action, stream, body)
	if err == nil || requestedModel == "" || account == nil || !IsModelUnavailableFailover(err) {
		return result, err
	}

	chain := []string{requestedModel}
	if s != nil && s.settingService != nil {
		chain = s.settingService.BuildModelFallbackChain(ctx, PlatformGemini, requestedModel)
	}
	lastErr := err
	for _, candidate := range chain[1:] {
		result, err = s.forwardNativeOnce(ctx, c, account, candidate, action, stream, body)
		if err == nil {
			if result != nil {
				result.Model = requestedModel
			}
			return result, nil
		}
		lastErr = err
		if !IsModelUnavailableFailover(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

package service

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openAIModelForward func(body []byte) (*OpenAIForwardResult, error)

func mappedModelHintForFallbackAttempt(originalBody, attemptBody []byte, defaultMappedModel string) string {
	originalModel := strings.TrimSpace(gjson.GetBytes(originalBody, "model").String())
	attemptModel := strings.TrimSpace(gjson.GetBytes(attemptBody, "model").String())
	if originalModel != "" && attemptModel != "" && attemptModel != originalModel {
		return ""
	}
	return defaultMappedModel
}

func (s *OpenAIGatewayService) forwardWithSameAccountModelFallback(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	forward openAIModelForward,
) (*OpenAIForwardResult, error) {
	requestedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	result, err := forward(body)
	if err == nil || requestedModel == "" || account == nil || !IsModelUnavailableFailover(err) {
		return result, err
	}

	chain := []string{requestedModel}
	if s != nil && s.settingService != nil {
		chain = s.settingService.BuildModelFallbackChain(ctx, account.Platform, requestedModel)
	}
	lastErr := err
	for _, candidate := range chain[1:] {
		result, err = forward(ReplaceModelInBody(body, candidate))
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
		if !IsModelUnavailableFailover(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

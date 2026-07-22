package service

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Forward exhausts the configured Antigravity model chain on the selected
// account before returning control to the handler's account failover loop.
func (s *AntigravityGatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, body []byte, isStickySession bool) (*ForwardResult, error) {
	requestedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	result, err := s.forwardOnce(ctx, c, account, body, isStickySession)
	if err == nil || requestedModel == "" || account == nil || !IsModelUnavailableFailover(err) {
		return result, err
	}

	chain := []string{requestedModel}
	if s != nil && s.settingService != nil {
		chain = s.settingService.BuildModelFallbackChain(ctx, PlatformAntigravity, requestedModel)
	}
	lastErr := err
	for _, candidate := range chain[1:] {
		result, err = s.forwardOnce(ctx, c, account, ReplaceModelInBody(body, candidate), isStickySession)
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

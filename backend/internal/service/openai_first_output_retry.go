package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const openAIFirstOutputRetrySessionKey = "openai_first_output_retry_session"

// NewOpenAIFirstOutputRetrySelection builds a fresh slot-acquisition plan for
// an account whose previous attempt has already released its slot.
func (s *OpenAIGatewayService) NewOpenAIFirstOutputRetrySelection(account *Account) *AccountSelectionResult {
	if s == nil || account == nil {
		return nil
	}
	cfg := s.schedulingConfig()
	return &AccountSelectionResult{
		Account: account,
		WaitPlan: &AccountWaitPlan{
			AccountID:      account.ID,
			MaxConcurrency: account.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		},
	}
}

// SetOpenAIFirstOutputRetrySession overrides retry-scoped upstream session
// headers. The request body carries the same seed as prompt_cache_key.
func SetOpenAIFirstOutputRetrySession(c *gin.Context, seed string) {
	if c == nil {
		return
	}
	c.Set(openAIFirstOutputRetrySessionKey, strings.TrimSpace(seed))
}

func ClearOpenAIFirstOutputRetrySession(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(openAIFirstOutputRetrySessionKey, "")
}

func openAIFirstOutputRetrySession(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(openAIFirstOutputRetrySessionKey)
	if !ok {
		return ""
	}
	seed, _ := value.(string)
	return strings.TrimSpace(seed)
}

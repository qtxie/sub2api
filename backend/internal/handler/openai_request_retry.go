package handler

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

type openAIRequestRetryController struct {
	cfg        config.GatewayOpenAIRequestRetryConfig
	deadline   time.Time
	attempts   int
	retryRound int
	enabled    bool
}

type openAIRequestRetryWaitLimiter struct {
	max    int64
	active atomic.Int64
}

func newOpenAIRequestRetryWaitLimiter(max int) *openAIRequestRetryWaitLimiter {
	return &openAIRequestRetryWaitLimiter{max: int64(max)}
}

func requestRetryMaxWaitingRequests(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	return cfg.Gateway.OpenAIRequestRetry.MaxWaitingRequests
}

func (l *openAIRequestRetryWaitLimiter) Acquire() (func(), bool) {
	if l == nil || l.max <= 0 {
		return func() {}, true
	}
	for {
		active := l.active.Load()
		if active >= l.max {
			return nil, false
		}
		if l.active.CompareAndSwap(active, active+1) {
			return func() { l.active.Add(-1) }, true
		}
	}
}

func newOpenAIRequestRetryController(cfg config.GatewayOpenAIRequestRetryConfig, stream bool, now time.Time) *openAIRequestRetryController {
	enabled := cfg.Enabled && stream && cfg.TotalBudgetSeconds > 0
	return &openAIRequestRetryController{
		cfg:      cfg,
		deadline: now.Add(time.Duration(cfg.TotalBudgetSeconds) * time.Second),
		enabled:  enabled,
	}
}

func (r *openAIRequestRetryController) Enabled() bool {
	return r != nil && r.enabled
}

func (r *openAIRequestRetryController) RecordAttempt() {
	if r != nil && r.enabled {
		r.attempts++
	}
}

func (r *openAIRequestRetryController) CanAttempt(now time.Time) bool {
	if !r.Enabled() {
		return true
	}
	if !now.Before(r.deadline) {
		return false
	}
	return r.cfg.MaxAttempts == 0 || r.attempts < r.cfg.MaxAttempts
}

func (r *openAIRequestRetryController) Remaining(now time.Time) time.Duration {
	if !r.Enabled() {
		return 0
	}
	remaining := r.deadline.Sub(now)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (r *openAIRequestRetryController) NextWait(now, retryAt time.Time) (time.Duration, bool) {
	if !r.Enabled() || !r.CanAttempt(now) {
		return 0, false
	}
	delay := openAIRequestRetryBackoff(r.cfg, r.retryRound, rand.Float64())
	r.retryRound++
	if cooldownDelay := retryAt.Sub(now); cooldownDelay > delay {
		delay = cooldownDelay
	}
	remaining := r.Remaining(now)
	if remaining <= 0 {
		return 0, false
	}
	if delay > remaining {
		delay = remaining
	}
	return delay, true
}

func openAIRequestRetryBackoff(cfg config.GatewayOpenAIRequestRetryConfig, retryRound int, random float64) time.Duration {
	initial := time.Duration(cfg.BackoffInitialSeconds) * time.Second
	maximum := time.Duration(cfg.BackoffMaxSeconds) * time.Second
	if initial <= 0 {
		initial = 2 * time.Second
	}
	if maximum < initial {
		maximum = initial
	}
	delay := initial
	for i := 0; i < retryRound && delay < maximum; i++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		delay = maximum
	}
	jitter := cfg.JitterRatio
	if jitter <= 0 {
		return delay
	}
	if random < 0 {
		random = 0
	} else if random > 1 {
		random = 1
	}
	factor := 1 + (2*random-1)*jitter
	jittered := time.Duration(float64(delay) * factor)
	if jittered > maximum {
		return maximum
	}
	return jittered
}

func isOpenAIRequestRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		529:
		return true
	default:
		return false
	}
}

func (h *OpenAIGatewayHandler) waitForOpenAIRequestRetry(
	c *gin.Context,
	wait time.Duration,
	streamStarted *bool,
	sendHeartbeats bool,
) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	var heartbeat <-chan time.Time
	var ticker *time.Ticker
	if sendHeartbeats && h != nil && h.cfg != nil && h.cfg.Gateway.StreamKeepaliveInterval > 0 {
		ticker = time.NewTicker(time.Duration(h.cfg.Gateway.StreamKeepaliveInterval) * time.Second)
		heartbeat = ticker.C
		defer ticker.Stop()
	}

	for {
		select {
		case <-c.Request.Context().Done():
			return c.Request.Context().Err()
		case <-timer.C:
			return nil
		case <-heartbeat:
			if streamStarted != nil && !*streamStarted {
				c.Header("Content-Type", "text/event-stream")
				c.Header("Cache-Control", "no-cache")
				c.Header("Connection", "keep-alive")
				c.Header("X-Accel-Buffering", "no")
				*streamStarted = true
			}
			if _, err := fmt.Fprint(c.Writer, string(SSEPingFormatComment)); err != nil {
				return err
			}
			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				return http.ErrNotSupported
			}
			flusher.Flush()
		}
	}
}

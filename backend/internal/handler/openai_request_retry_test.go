//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func testOpenAIRequestRetryConfig() config.GatewayOpenAIRequestRetryConfig {
	return config.GatewayOpenAIRequestRetryConfig{
		Enabled:                  true,
		TotalBudgetSeconds:       30,
		MaxAttempts:              0,
		BackoffInitialSeconds:    2,
		BackoffMaxSeconds:        30,
		JitterRatio:              0.2,
		WaitForTemporaryCapacity: true,
	}
}

func TestOpenAIRequestRetryBackoffExponentialAndCapped(t *testing.T) {
	cfg := testOpenAIRequestRetryConfig()
	for retryRound, expected := range []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	} {
		require.Equal(t, expected, openAIRequestRetryBackoff(cfg, retryRound, 0.5))
	}
}

func TestOpenAIRequestRetryBackoffAppliesConfiguredJitter(t *testing.T) {
	cfg := testOpenAIRequestRetryConfig()
	require.Equal(t, 1600*time.Millisecond, openAIRequestRetryBackoff(cfg, 0, 0))
	require.Equal(t, 2400*time.Millisecond, openAIRequestRetryBackoff(cfg, 0, 1))
	require.Equal(t, 30*time.Second, openAIRequestRetryBackoff(cfg, 4, 1))
}

func TestOpenAIRequestRetryControllerCooldownPrecedesBackoffAndBudgetCapsWait(t *testing.T) {
	now := time.Date(2026, 7, 22, 13, 6, 0, 0, time.UTC)
	cfg := testOpenAIRequestRetryConfig()
	cfg.JitterRatio = 0
	controller := newOpenAIRequestRetryController(cfg, true, now)
	controller.RecordAttempt()

	wait, ok := controller.NextWait(now, now.Add(10*time.Second))
	require.True(t, ok)
	require.Equal(t, 10*time.Second, wait)

	wait, ok = controller.NextWait(now.Add(29*time.Second), time.Time{})
	require.True(t, ok)
	require.Equal(t, time.Second, wait)
	require.False(t, controller.CanAttempt(now.Add(30*time.Second)))
}

func TestOpenAIRequestRetryControllerOptionalAttemptLimit(t *testing.T) {
	now := time.Now()
	cfg := testOpenAIRequestRetryConfig()
	cfg.MaxAttempts = 2
	controller := newOpenAIRequestRetryController(cfg, true, now)

	controller.RecordAttempt()
	require.True(t, controller.CanAttempt(now))
	controller.RecordAttempt()
	require.False(t, controller.CanAttempt(now))
	_, ok := controller.NextWait(now, time.Time{})
	require.False(t, ok)
}

func TestOpenAIRequestRetryControllerOnlyEnablesForStreamingResponses(t *testing.T) {
	cfg := testOpenAIRequestRetryConfig()
	require.True(t, newOpenAIRequestRetryController(cfg, true, time.Now()).Enabled())
	require.False(t, newOpenAIRequestRetryController(cfg, false, time.Now()).Enabled())
}

func TestOpenAIRequestRetryableStatusOnlyIncludesTransientFailures(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504, 529} {
		require.True(t, isOpenAIRequestRetryableStatus(status), "status=%d", status)
	}
	for _, status := range []int{400, 401, 403, 404, 422} {
		require.False(t, isOpenAIRequestRetryableStatus(status), "status=%d", status)
	}
}

func TestOpenAIRequestRetryWaitLimiterBoundsAndReleases(t *testing.T) {
	limiter := newOpenAIRequestRetryWaitLimiter(1)
	release, ok := limiter.Acquire()
	require.True(t, ok)
	_, ok = limiter.Acquire()
	require.False(t, ok)
	release()
	_, ok = limiter.Acquire()
	require.True(t, ok)
}

func TestWaitForOpenAIRequestRetrySendsSSEHeartbeats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	h := &OpenAIGatewayHandler{cfg: &config.Config{}}
	h.cfg.Gateway.StreamKeepaliveInterval = 1
	streamStarted := false

	require.NoError(t, h.waitForOpenAIRequestRetry(c, 1100*time.Millisecond, &streamStarted, true))
	require.True(t, streamStarted)
	require.Contains(t, rec.Body.String(), string(SSEPingFormatComment))
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
}

func TestWaitForOpenAIRequestRetryStopsOnClientCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	cancel()

	err := (&OpenAIGatewayHandler{}).waitForOpenAIRequestRetry(c, time.Minute, new(bool), true)
	require.ErrorIs(t, err, context.Canceled)
}

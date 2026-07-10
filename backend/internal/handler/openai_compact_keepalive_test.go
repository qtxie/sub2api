//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestEnsureForwardErrorResponse_AfterCompactKeepaliveCommitEmitsResponsesFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	service.MarkOpenAICompactClientStream(c)
	stop := service.StartOpenAICompactSSEKeepalive(c, 10*time.Millisecond)
	defer stop()
	time.Sleep(25 * time.Millisecond)

	h := &OpenAIGatewayHandler{}
	require.True(t, h.ensureForwardErrorResponse(c, false))

	require.Equal(t, http.StatusOK, rec.Code)
	events := parseCompactHandlerSSE(t, stripCompactKeepaliveComments(rec.Body.String()))
	require.Len(t, events, 1)
	require.Equal(t, "response.failed", events[0][0])
	require.Equal(t, "upstream_error", gjson.Get(events[0][1], "response.error.code").String())
	require.Contains(t, gjson.Get(events[0][1], "response.error.message").String(), "Upstream request failed")

	streamErr, ok := service.GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, http.StatusBadGateway, streamErr.IntendedStatus)
}

func stripCompactKeepaliveComments(body string) string {
	var blocks []string
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if strings.HasPrefix(strings.TrimSpace(block), ":") {
			continue
		}
		blocks = append(blocks, block)
	}
	return strings.Join(blocks, "\n\n")
}

func parseCompactHandlerSSE(t *testing.T, body string) [][2]string {
	t.Helper()
	var events [][2]string
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		lines := strings.Split(block, "\n")
		require.Len(t, lines, 2, "each SSE event should have event and data lines: %q", block)
		require.True(t, strings.HasPrefix(lines[0], "event: "), "missing event line: %q", block)
		require.True(t, strings.HasPrefix(lines[1], "data: "), "missing data line: %q", block)
		events = append(events, [2]string{
			strings.TrimPrefix(lines[0], "event: "),
			strings.TrimPrefix(lines[1], "data: "),
		})
	}
	return events
}

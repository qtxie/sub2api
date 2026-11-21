//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 流式 cyber 命中在客户端尚未收到任何语义字节时必须转成可降级 failover 错误，
// 交由 handler 先做同账号换模型、再跨账号 failover。回归：AdjustedWrittenSize 的
// -1（未写出任何字节）状态曾被 `== 0` 判据永久挡在透传分支，cyber 阻断原样直达
// Codex 客户端（2026-11-20 22:59 线上事故），且与 openAIStreamClientOutputStarted
// 的 `>= 0` 语义互相矛盾导致该分支为死代码。
func TestHandleStreamingCyberPolicyPreOutputConvertsToFailoverError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 59, Platform: PlatformOpenAI}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: ioNopCloserString(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp\"}}\n\n" +
				"data: {\"type\":\"response.failed\",\"error\":{\"code\":\"cyber_policy\",\"message\":\"This content was flagged for possible cybersecurity risk.\"}}\n\n",
		),
	}
	_, err := svc.handleStreamingResponseWithReasoningAndTimeout(
		context.Background(), resp, c, account, time.Now(), "gpt-6.2-astra", "gpt-6.2-astra", "", 0,
	)
	require.True(t, IsCyberPolicyFailoverError(err), "pre-output cyber block must convert to a failover error, got %v", err)
	require.Zero(t, recorder.Body.Len(), "no semantic bytes may reach the client before failover")
	mark := GetOpsCyberPolicy(c)
	require.NotNil(t, mark, "mark must ride along so the landing hit is audited/billed")
	require.Equal(t, http.StatusOK, mark.UpstreamStatus)
}

// 已有语义输出后 cyber 命中无法在同一条流上重放，维持原样透传（不 failover）。
func TestHandleStreamingCyberPolicyAfterOutputPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 59, Platform: PlatformOpenAI}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: ioNopCloserString(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial answer\"}\n\n" +
				"data: {\"type\":\"response.failed\",\"error\":{\"code\":\"cyber_policy\",\"message\":\"This content was flagged for possible cybersecurity risk.\"}}\n\n",
		),
	}
	_, err := svc.handleStreamingResponseWithReasoningAndTimeout(
		context.Background(), resp, c, account, time.Now(), "gpt-6.2-astra", "gpt-6.2-astra", "", 0,
	)
	require.False(t, IsCyberPolicyFailoverError(err), "post-output cyber block must not be replayed as failover")
	require.NotNil(t, GetOpsCyberPolicy(c), "passthrough hit must still be audited")
	require.Contains(t, recorder.Body.String(), "cyber_policy")
	require.Contains(t, recorder.Body.String(), "partial answer")
}

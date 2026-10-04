package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 客户端断开并不等于上游有责：只有上游真实返回 4xx/5xx（如 input.codes 的
// Cloudflare 524）才应记到账号头上，否则用户主动取消会把健康账号挤出调度。
func TestOpenAIUpstreamAttributableStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantOK     bool
	}{
		{
			name: "上游 524 可归责",
			// 524 = Cloudflare 边缘放弃等待源站，正是本次漏计费的上游症状。
			err:        &service.UpstreamFailoverError{StatusCode: 524},
			wantStatus: 524,
			wantOK:     true,
		},
		{
			name:       "上游 500 可归责",
			err:        &service.UpstreamFailoverError{StatusCode: http.StatusInternalServerError},
			wantStatus: http.StatusInternalServerError,
			wantOK:     true,
		},
		{
			name: "上游 429 可归责",
			err: &service.UpstreamFailoverError{
				StatusCode: http.StatusTooManyRequests,
			},
			wantStatus: http.StatusTooManyRequests,
			wantOK:     true,
		},
		{
			name:   "纯客户端取消不可归责",
			err:    context.Canceled,
			wantOK: false,
		},
		{
			name:   "包装后的客户端取消不可归责",
			err:    fmt.Errorf("stream usage incomplete: %w", context.Canceled),
			wantOK: false,
		},
		{
			name:   "无上游状态码的 failover 不可归责",
			err:    &service.UpstreamFailoverError{},
			wantOK: false,
		},
		{
			name:   "nil 不崩",
			err:    nil,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, ok := openAIUpstreamAttributableStatus(tt.err)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantStatus, status)
		})
	}
}

func TestReportOpenAIGoneAccountFailure_NilSafe(t *testing.T) {
	var h *OpenAIGatewayHandler
	// 所有入参为零值/空时都必须静默返回，不得 panic（这些分支运行在请求已经
	// 取消的热路径上，panic 会连带打掉整个请求 goroutine）。
	h.reportOpenAIGoneAccountFailure(nil, nil, "", nil)

	h = &OpenAIGatewayHandler{}
	h.reportOpenAIGoneAccountFailure(&service.Account{ID: 59}, nil, "gpt-5.6-terra", context.Canceled)
	h.reportOpenAIGoneAccountFailure(nil, nil, "", &service.UpstreamFailoverError{StatusCode: 524})
}

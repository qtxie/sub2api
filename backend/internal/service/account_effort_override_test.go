package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 账号级出站 effort 覆写（credentials["reasoning_effort_override"]）：Responses
// 协议路径的出站请求体中已存在的 effort 字段一律改写为账号配置值，覆盖客户端
// 请求档位。作用点为 buildUpstreamRequest 出站点（/v1/responses 主路径及
// messages / chat_completions 入站向 Responses 上游的转换）、计费提取前收敛与
// WS 组策略之后；Chat Completions 出站不覆写。此处覆盖纯函数语义，端到端走线
// 由下方 Forward 用例验证。

func TestGetReasoningEffortOverride(t *testing.T) {
	t.Run("nil_account_returns_empty", func(t *testing.T) {
		require.Empty(t, (*Account)(nil).GetReasoningEffortOverride())
	})

	t.Run("missing_key_returns_empty", func(t *testing.T) {
		require.Empty(t, (&Account{Credentials: map[string]any{"api_key": "sk-test"}}).GetReasoningEffortOverride())
	})

	t.Run("non_string_value_returns_empty", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIReasoningEffortOverrideCredentialKey: true}}
		require.Empty(t, account.GetReasoningEffortOverride())
	})

	t.Run("empty_string_returns_empty", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIReasoningEffortOverrideCredentialKey: "  "}}
		require.Empty(t, account.GetReasoningEffortOverride())
	})

	t.Run("invalid_value_returns_empty", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIReasoningEffortOverrideCredentialKey: "super"}}
		require.Empty(t, account.GetReasoningEffortOverride())
	})

	t.Run("canonical_values", func(t *testing.T) {
		for _, value := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
			account := &Account{Credentials: map[string]any{openAIReasoningEffortOverrideCredentialKey: value}}
			require.Equal(t, value, account.GetReasoningEffortOverride())
		}
	})

	t.Run("normalizes_case_and_separators", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIReasoningEffortOverrideCredentialKey: " Extra_High "}}
		require.Equal(t, "xhigh", account.GetReasoningEffortOverride())
	})
}

func TestApplyAccountReasoningEffortOverride(t *testing.T) {
	newOverrideAccount := func(value string) *Account {
		return &Account{Credentials: map[string]any{openAIReasoningEffortOverrideCredentialKey: value}}
	}

	t.Run("nil_account_unchanged", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","reasoning":{"effort":"low"}}`)
		got, changed := applyAccountReasoningEffortOverride(nil, body)
		require.False(t, changed)
		require.Equal(t, string(body), string(got))
	})

	t.Run("no_config_unchanged", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","reasoning":{"effort":"low"}}`)
		got, changed := applyAccountReasoningEffortOverride(&Account{Credentials: map[string]any{}}, body)
		require.False(t, changed)
		require.Equal(t, string(body), string(got))
	})

	t.Run("invalid_config_unchanged", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","reasoning_effort":"low"}`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("super"), body)
		require.False(t, changed)
		require.Equal(t, string(body), string(got))
	})

	t.Run("empty_body_unchanged", func(t *testing.T) {
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("high"), nil)
		require.False(t, changed)
		require.Equal(t, "", string(got))
	})

	t.Run("overrides_chat_completions_field", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","reasoning_effort":"low","temperature":0.5}`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("high"), body)
		require.True(t, changed)
		require.Equal(t, "high", gjson.GetBytes(got, "reasoning_effort").String())
		require.Equal(t, 0.5, gjson.GetBytes(got, "temperature").Float())
		require.Equal(t, "gpt-5", gjson.GetBytes(got, "model").String())
	})

	t.Run("overrides_responses_field", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","input":"hi","reasoning":{"effort":"minimal","summary":"auto"}}`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("max"), body)
		require.True(t, changed)
		require.Equal(t, "max", gjson.GetBytes(got, "reasoning.effort").String())
		// reasoning 兄弟字段不受影响。
		require.Equal(t, "auto", gjson.GetBytes(got, "reasoning.summary").String())
	})

	t.Run("overrides_output_config_field", func(t *testing.T) {
		body := []byte(`{"model":"gpt-6-sol","output_config":{"effort":"low","verbosity":"high"}}`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("xhigh"), body)
		require.True(t, changed)
		require.Equal(t, "xhigh", gjson.GetBytes(got, "output_config.effort").String())
		require.Equal(t, "high", gjson.GetBytes(got, "output_config.verbosity").String())
	})

	t.Run("overrides_all_present_paths", func(t *testing.T) {
		body := []byte(`{"reasoning_effort":"low","reasoning":{"effort":"low"},"output_config":{"effort":"low"}}`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("medium"), body)
		require.True(t, changed)
		require.Equal(t, "medium", gjson.GetBytes(got, "reasoning_effort").String())
		require.Equal(t, "medium", gjson.GetBytes(got, "reasoning.effort").String())
		require.Equal(t, "medium", gjson.GetBytes(got, "output_config.effort").String())
	})

	t.Run("absent_fields_unchanged", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","input":"hi"}`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("high"), body)
		require.False(t, changed)
		require.Equal(t, string(body), string(got))
	})

	t.Run("non_string_field_rewritten", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","reasoning_effort":3}`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("high"), body)
		require.True(t, changed)
		require.Equal(t, "high", gjson.GetBytes(got, "reasoning_effort").String())
	})

	t.Run("same_value_is_noop", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","reasoning_effort":"high"}`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("high"), body)
		require.False(t, changed)
		require.Equal(t, string(body), string(got))
	})

	t.Run("idempotent", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","reasoning":{"effort":"low"}}`)
		once, changed := applyAccountReasoningEffortOverride(newOverrideAccount("high"), body)
		require.True(t, changed)
		twice, changedAgain := applyAccountReasoningEffortOverride(newOverrideAccount("high"), once)
		require.False(t, changedAgain)
		require.Equal(t, string(once), string(twice))
	})

	t.Run("invalid_json_unchanged", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5",`)
		got, changed := applyAccountReasoningEffortOverride(newOverrideAccount("high"), body)
		require.False(t, changed)
		require.Equal(t, string(body), string(got))
	})
}

func TestNormalizeExtraBodyCredentials_ReasoningEffortOverride(t *testing.T) {
	key := openAIReasoningEffortOverrideCredentialKey

	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{key: "high"}))
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{key: "xhigh"}))
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{key: "max"}))
	// 空串 = 清除/不覆写，放行（前端选择"不覆写"即删除该键或提交空值）。
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{key: ""}))
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{key: nil}))

	// 非法值必须拒绝，避免静默失效。
	require.Error(t, NormalizeExtraBodyCredentials(map[string]any{key: "super"}))
	require.Error(t, NormalizeExtraBodyCredentials(map[string]any{key: true}))
	require.Error(t, NormalizeExtraBodyCredentials(map[string]any{key: 3}))
}

// 账号级 effort 覆写仅作用于 Responses 协议转发路径（buildUpstreamRequest 出站
// 点 + 计费提取前收敛）；Chat Completions 出站链路（strip/extra_body）不含覆写
// 步骤。
func TestAccountOutboundEffortPolicyScope(t *testing.T) {
	t.Run("cc_outbound_not_overridden", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{
			openAIReasoningEffortOverrideCredentialKey: "high",
		}}
		body := []byte(`{"model":"m","reasoning_effort":"low"}`)
		got := stripAccountReasoningEffort(account, body)
		got = applyAccountExtraBody(account, got)
		// CC 出站链路不含覆写步骤：值保持客户端原样。
		require.Equal(t, "low", gjson.GetBytes(got, "reasoning_effort").String())
	})
}

// WS 入站 turn：组策略之后叠加账号级 effort 覆写。
func TestApplyOpenAIWSReasoningEffortPolicy_AccountOverride(t *testing.T) {
	account := &Account{Credentials: map[string]any{openAIReasoningEffortOverrideCredentialKey: "high"}}
	payload := []byte(`{"type":"response.create","model":"gpt-5-codex","reasoning":{"effort":"low"}}`)

	t.Run("override_applied_without_group_policy", func(t *testing.T) {
		got, err := applyOpenAIWSReasoningEffortPolicy(payload, nil, account)
		require.NoError(t, err)
		require.Equal(t, "high", gjson.GetBytes(got, "reasoning.effort").String())
	})

	t.Run("override_applied_after_group_policy", func(t *testing.T) {
		hooks := &OpenAIWSIngressHooks{MaxReasoningEffort: "medium"}
		got, err := applyOpenAIWSReasoningEffortPolicy(payload, hooks, account)
		require.NoError(t, err)
		require.Equal(t, "high", gjson.GetBytes(got, "reasoning.effort").String())
	})

	t.Run("no_override_no_group_policy_unchanged", func(t *testing.T) {
		got, err := applyOpenAIWSReasoningEffortPolicy(payload, nil, &Account{})
		require.NoError(t, err)
		require.Equal(t, string(payload), string(got))
	})
}

// 端到端（Responses 协议路径）：/v1/responses 入站直转 Responses 上游时，账号级
// effort 覆写必须出现在上游请求体与计费 effort 里。
func TestForwardResponses_AccountEffortOverrideReachesResponsesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	responsesBody := []byte(`{"model":"gpt-5.4","stream":false,"input":"hi","reasoning":{"effort":"low"}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(responsesBody))

	newOverrideAccount := func() *Account {
		return &Account{
			ID:       78,
			Name:     "effort-override",
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://relay.example",
				openAIReasoningEffortOverrideCredentialKey: "high",
			},
		}
	}

	newTestService := func(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
		return &OpenAIGatewayService{
			cfg: &config.Config{Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{Enabled: false},
			}},
			httpUpstream: upstream,
		}
	}

	t.Run("configured_override_reaches_upstream", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"resp_effort_override","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		}}

		result, err := newTestService(upstream).Forward(context.Background(), c, newOverrideAccount(), responsesBody)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Contains(t, upstream.lastReq.URL.String(), "/responses")
		// 覆写值到达上游。
		require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		// 计费用 effort 与出站一致。
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "high", *result.ReasoningEffort)
	})

	t.Run("no_override_keeps_client_effort", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"resp_no_override","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		}}
		account := newOverrideAccount()
		delete(account.Credentials, openAIReasoningEffortOverrideCredentialKey)

		_, err := newTestService(upstream).Forward(context.Background(), c, account, responsesBody)
		require.NoError(t, err)
		require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	})
}

// 端到端（Chat Completions 回退路径）：账号强制走 CC 上游时，effort 覆写不生效，
// 客户端 effort 原样转发。
func TestForwardResponses_AccountEffortOverrideSkippedOnChatCompletionsFallback(t *testing.T) {
	responsesBody := []byte(`{"model":"Ling-3.1-flash","stream":false,"input":"hi","reasoning":{"effort":"low"}}`)

	newForceCCAccount := func() *Account {
		return &Account{
			ID:       79,
			Name:     "ant-force-cc",
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
			},
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "http://upstream.example",
				openAIReasoningEffortOverrideCredentialKey: "high",
			},
			Status:      StatusActive,
			Schedulable: true,
		}
	}

	c := newDeepSeekChatFallbackContext(t, responsesBody)
	upstream := newOKChatCompletionsUpstream("rid_effort_override_cc_skip", deepSeekChatFallbackOKBody)
	svc := &OpenAIGatewayService{
		cfg:          deepSeekChatFallbackTestConfig(),
		httpUpstream: upstream,
	}

	_, err := svc.Forward(context.Background(), c, newForceCCAccount(), responsesBody)
	require.NoError(t, err)
	require.Contains(t, upstream.lastReq.URL.String(), "/chat/completions")
	// CC 出站不覆写：桥接后的 reasoning_effort 保持客户端档位。
	require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
}

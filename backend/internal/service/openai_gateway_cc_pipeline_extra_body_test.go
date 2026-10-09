package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// extra_body 是账号级出站 body 附加字段（credentials["extra_body"]）：用于协议
// 差异补齐——部分上游不认 OpenAI 标准的 reasoning_effort，只认自有非标字段
// （如 Ling/Kimi/GLM 的 thinking: {"type":"enabled"}），而 Responses→CC 桥接的
// 结构体重建会丢掉这类客户端字段，此时由管理员在账号上显式声明。
// 注入点在 sendCCUpstreamRequest，三条 CC forwarder（原生 CC 直转 /
// Responses→CC 回退 / messages→CC 回退）共用；passthrough 路径不受影响。

func TestGetOpenAIExtraBody(t *testing.T) {
	t.Run("nil_account_returns_nil", func(t *testing.T) {
		require.Nil(t, (*Account)(nil).GetOpenAIExtraBody())
	})

	t.Run("missing_key_returns_nil", func(t *testing.T) {
		require.Nil(t, (&Account{Credentials: map[string]any{"api_key": "sk-test"}}).GetOpenAIExtraBody())
	})

	t.Run("non_object_value_returns_nil", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIExtraBodyCredentialKey: "enabled"}}
		require.Nil(t, account.GetOpenAIExtraBody())
	})

	t.Run("empty_object_returns_nil", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIExtraBodyCredentialKey: map[string]any{}}}
		require.Nil(t, account.GetOpenAIExtraBody())
	})

	t.Run("valid_object_returned", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{
			openAIExtraBodyCredentialKey: map[string]any{
				"thinking": map[string]any{"type": "enabled"},
			},
		}}
		got := account.GetOpenAIExtraBody()
		require.Len(t, got, 1)
		require.Equal(t, "enabled", got["thinking"].(map[string]any)["type"])
	})
}

func TestNormalizeExtraBodyCredentials(t *testing.T) {
	require.NoError(t, NormalizeExtraBodyCredentials(nil))
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{}))
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{"api_key": "sk-test"}))
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{
		openAIExtraBodyCredentialKey: map[string]any{"thinking": map[string]any{"type": "enabled"}},
	}))
	// 空对象等价不注入，放行（前端清空输入即提交空对象或删除该键）。
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{openAIExtraBodyCredentialKey: map[string]any{}}))

	// 非对象值必须拒绝，避免静默失效。
	require.Error(t, NormalizeExtraBodyCredentials(map[string]any{openAIExtraBodyCredentialKey: "enabled"}))
	require.Error(t, NormalizeExtraBodyCredentials(map[string]any{openAIExtraBodyCredentialKey: 1}))
	require.Error(t, NormalizeExtraBodyCredentials(map[string]any{openAIExtraBodyCredentialKey: []any{"x"}}))

	// strip_reasoning_effort 必须是布尔值。
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{openAIStripReasoningEffortCredentialKey: true}))
	require.NoError(t, NormalizeExtraBodyCredentials(map[string]any{openAIStripReasoningEffortCredentialKey: false}))
	require.Error(t, NormalizeExtraBodyCredentials(map[string]any{openAIStripReasoningEffortCredentialKey: "yes"}))
}

func TestStripAccountReasoningEffort(t *testing.T) {
	base := []byte(`{"model":"Ling-3.1-flash","reasoning_effort":"medium","temperature":0.5}`)

	t.Run("flag_on_strips_field", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIStripReasoningEffortCredentialKey: true}}
		got := stripAccountReasoningEffort(account, base)
		require.False(t, gjson.GetBytes(got, "reasoning_effort").Exists())
		require.Equal(t, "Ling-3.1-flash", gjson.GetBytes(got, "model").String())
		require.Equal(t, 0.5, gjson.GetBytes(got, "temperature").Float())
	})

	t.Run("flag_off_unchanged", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIStripReasoningEffortCredentialKey: false}}
		require.Equal(t, string(base), string(stripAccountReasoningEffort(account, base)))
	})

	t.Run("nil_account_unchanged", func(t *testing.T) {
		require.Equal(t, string(base), string(stripAccountReasoningEffort(nil, base)))
	})

	t.Run("field_absent_unchanged", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIStripReasoningEffortCredentialKey: true}}
		body := []byte(`{"model":"m","temperature":0.5}`)
		require.Equal(t, string(body), string(stripAccountReasoningEffort(account, body)))
	})

	t.Run("strip_then_inject_explicit_value_wins", func(t *testing.T) {
		// 出站顺序：先剥离后注入——extra_body 显式写入的 reasoning_effort 优先。
		account := &Account{Credentials: map[string]any{
			openAIStripReasoningEffortCredentialKey: true,
			openAIExtraBodyCredentialKey:            map[string]any{"reasoning_effort": "high"},
		}}
		got := applyAccountExtraBody(account, stripAccountReasoningEffort(account, base))
		require.Equal(t, "high", gjson.GetBytes(got, "reasoning_effort").String())
	})
}

func TestApplyAccountExtraBody(t *testing.T) {
	base := []byte(`{"model":"Ling-3.1-flash","reasoning_effort":"medium","temperature":0.5}`)

	t.Run("nil_account_unchanged", func(t *testing.T) {
		require.Equal(t, string(base), string(applyAccountExtraBody(nil, base)))
	})

	t.Run("no_config_unchanged", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{"api_key": "sk-test"}}
		require.Equal(t, string(base), string(applyAccountExtraBody(account, base)))
	})

	t.Run("invalid_config_unchanged", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{openAIExtraBodyCredentialKey: "enabled"}}
		require.Equal(t, string(base), string(applyAccountExtraBody(account, base)))
	})

	t.Run("injects_nested_object", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{
			openAIExtraBodyCredentialKey: map[string]any{
				"thinking": map[string]any{"type": "enabled"},
			},
		}}
		got := applyAccountExtraBody(account, base)
		require.Equal(t, "enabled", gjson.GetBytes(got, "thinking.type").String())
		// 原有字段不受影响。
		require.Equal(t, "medium", gjson.GetBytes(got, "reasoning_effort").String())
		require.Equal(t, 0.5, gjson.GetBytes(got, "temperature").Float())
	})

	t.Run("overrides_client_field", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{
			openAIExtraBodyCredentialKey: map[string]any{"temperature": 1},
		}}
		got := applyAccountExtraBody(account, base)
		require.Equal(t, 1.0, gjson.GetBytes(got, "temperature").Float())
	})

	t.Run("empty_body_unchanged", func(t *testing.T) {
		account := &Account{Credentials: map[string]any{
			openAIExtraBodyCredentialKey: map[string]any{"thinking": map[string]any{"type": "enabled"}},
		}}
		require.Equal(t, "", string(applyAccountExtraBody(account, nil)))
	})
}

// 端到端：/v1/responses 入站经 CC 回退转发时，extra_body 配置必须出现在上游
// 请求体里（Ant/Ling 场景：Codex CLI 只会发 reasoning.effort，thinking 由账号
// 配置补齐）。复用 openai_gateway_deepseek_chat_reasoning_test.go 的 stub 上游
// harness（newDeepSeekChatFallbackContext / newOKChatCompletionsUpstream）。
func TestForwardResponses_ExtraBodyInjectedIntoChatCompletionsUpstream(t *testing.T) {
	responsesBody := []byte(`{"model":"Ling-3.1-flash","stream":false,"input":"hi","reasoning":{"effort":"medium"}}`)

	newExtraBodyAccountWithStrip := func(extra map[string]any, strip bool) *Account {
		creds := map[string]any{
			"api_key":  "sk-test",
			"base_url": "http://upstream.example",
		}
		if extra != nil {
			creds[openAIExtraBodyCredentialKey] = extra
		}
		if strip {
			creds[openAIStripReasoningEffortCredentialKey] = true
		}
		return &Account{
			ID:       76,
			Name:     "ant-extra-body",
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
			},
			Credentials: creds,
			Status:      StatusActive,
			Schedulable: true,
		}
	}
	newExtraBodyAccount := func(extra map[string]any) *Account {
		return newExtraBodyAccountWithStrip(extra, false)
	}

	newTestService := func(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
		return &OpenAIGatewayService{
			cfg:          deepSeekChatFallbackTestConfig(),
			httpUpstream: upstream,
		}
	}

	t.Run("configured_thinking_reaches_upstream", func(t *testing.T) {
		c := newDeepSeekChatFallbackContext(t, responsesBody)
		upstream := newOKChatCompletionsUpstream("rid_extra_body_on", deepSeekChatFallbackOKBody)
		account := newExtraBodyAccount(map[string]any{
			"thinking": map[string]any{"type": "enabled"},
		})

		result, err := newTestService(upstream).Forward(context.Background(), c, account, responsesBody)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Contains(t, upstream.lastReq.URL.String(), "/chat/completions")
		// 注入字段到达上游。
		require.Equal(t, "enabled", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
		// 桥接自带的 reasoning_effort 映射不受影响。
		require.Equal(t, "medium", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
		require.Equal(t, "Ling-3.1-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	})

	t.Run("no_extra_body_keeps_upstream_body_clean", func(t *testing.T) {
		c := newDeepSeekChatFallbackContext(t, responsesBody)
		upstream := newOKChatCompletionsUpstream("rid_extra_body_off", deepSeekChatFallbackOKBody)
		account := newExtraBodyAccount(nil)

		_, err := newTestService(upstream).Forward(context.Background(), c, account, responsesBody)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(upstream.lastBody, "thinking").Exists())
		require.Equal(t, "medium", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	})

	t.Run("extra_body_overrides_client_field", func(t *testing.T) {
		body := []byte(`{"model":"Ling-3.1-flash","stream":false,"input":"hi","temperature":0.5}`)
		c := newDeepSeekChatFallbackContext(t, body)
		upstream := newOKChatCompletionsUpstream("rid_extra_body_override", deepSeekChatFallbackOKBody)
		account := newExtraBodyAccount(map[string]any{"temperature": 1})

		_, err := newTestService(upstream).Forward(context.Background(), c, account, body)
		require.NoError(t, err)
		require.Equal(t, 1.0, gjson.GetBytes(upstream.lastBody, "temperature").Float())
	})

	t.Run("strip_flag_removes_reasoning_effort_upstream", func(t *testing.T) {
		c := newDeepSeekChatFallbackContext(t, responsesBody)
		upstream := newOKChatCompletionsUpstream("rid_extra_body_strip", deepSeekChatFallbackOKBody)
		account := newExtraBodyAccountWithStrip(nil, true)

		_, err := newTestService(upstream).Forward(context.Background(), c, account, responsesBody)
		require.NoError(t, err)
		// 客户端发了 reasoning.effort（桥接为 reasoning_effort），但剥离开关生效。
		require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning_effort").Exists())
		require.Equal(t, "Ling-3.1-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	})
}

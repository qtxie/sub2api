package service

import (
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIReasoningEffortOverrideCredentialKey 存储账号级出站 effort 覆写配置
// （credentials["reasoning_effort_override"]）：配置后，Responses 协议转发路径
// 的出站请求体中已存在的 effort 字段一律改写为该值，无视客户端请求的档位。
// 用于把账号流量收敛到上游唯一可用的 effort（部分上游只接受特定档位），或对
// 账号做成本/质量收敛。
//
// 作用范围：仅 Responses 协议转发路径——buildUpstreamRequest 出站点收口
// （覆盖 /v1/responses 主路径及 messages / chat_completions 入站向 Responses
// 上游的转换路径）与 WS turn 组策略之后。Chat Completions 协议转发路径
// （sendCCUpstreamRequest）不覆写：各兼容上游的 effort 语义由组策略、
// strip_reasoning_effort 与 extra_body 管理，账号级强制覆写仅服务于
// Responses 协议账号。
//
// 客户端未请求 effort 时字段不存在，覆写不注入（保持上游默认）；需要注入的
// 管理员应使用 extra_body 显式写入。
const openAIReasoningEffortOverrideCredentialKey = "reasoning_effort_override"

// accountReasoningEffortOverridePaths 列出出站 body 中可能承载 effort 的字段
// 路径，覆盖 Chat Completions 形状（reasoning_effort）、Responses 形状
// （reasoning.effort）与 output_config 形状（output_config.effort），与组策略
// ApplyReasoningEffortPolicy 的作用面保持一致。
var accountReasoningEffortOverridePaths = []string{"reasoning_effort", "reasoning.effort", "output_config.effort"}

// GetReasoningEffortOverride 返回账号配置的出站 effort 覆写值。存储于
// credentials["reasoning_effort_override"]，取值受支持集合 minimal/low/
// medium/high/xhigh/max（大小写、连字符与下划线不敏感）。缺失、非字符串、
// 空串或非法值返回 ""（不覆写）。
func (a *Account) GetReasoningEffortOverride() string {
	if a == nil {
		return ""
	}
	raw, ok := a.Credentials[openAIReasoningEffortOverrideCredentialKey].(string)
	if !ok {
		return ""
	}
	return NormalizeMaxReasoningEffort(raw)
}

// applyAccountReasoningEffortOverride 按账号开关覆写出站 body 的 effort 字段：
// 对 body 中已存在的 reasoning_effort / reasoning.effort / output_config.effort
// 一律改写为账号配置值。字段不存在时字节不变（客户端未请求的 effort 保持上游
// 默认，需要注入请用 extra_body）。单路径写失败仅跳过该路径（fail-open），
// 不阻断转发。重复执行幂等（failover 重试同账号重发同 body）。
// 返回 (body, changed)；changed 仅在确实发生改写时为 true。
func applyAccountReasoningEffortOverride(account *Account, body []byte) ([]byte, bool) {
	if account == nil || len(body) == 0 {
		return body, false
	}
	override := account.GetReasoningEffortOverride()
	if override == "" {
		return body, false
	}
	updated := body
	changed := false
	for _, path := range accountReasoningEffortOverridePaths {
		if !gjson.GetBytes(updated, path).Exists() {
			continue
		}
		next, err := sjson.SetBytes(updated, path, override)
		if err != nil {
			continue
		}
		if !changed && next != nil && string(next) != string(updated) {
			changed = true
		}
		updated = next
	}
	if !changed {
		return body, false
	}
	return updated, true
}

// normalizeReasoningEffortOverrideCredential 校验 credentials 中的 effort 覆写
// 配置：必须是受支持的 effort 值或空串（空串 = 清除/不覆写）。非字符串、
// 非空但无法归一化为合法值（如 "super"）时报 400——避免静默失效让管理员误以为
// 配置已生效。供账号创建/更新/批量更新的保存路径统一调用。
func normalizeReasoningEffortOverrideCredential(credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	raw, ok := credentials[openAIReasoningEffortOverrideCredentialKey]
	if !ok || raw == nil {
		return nil
	}
	value, isString := raw.(string)
	if !isString {
		return infraerrors.New(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
			"reasoning_effort_override must be a string")
	}
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if NormalizeMaxReasoningEffort(value) == "" {
		return infraerrors.New(http.StatusBadRequest, "INVALID_REASONING_EFFORT_OVERRIDE",
			"reasoning_effort_override must be one of minimal, low, medium, high, xhigh, max")
	}
	return nil
}

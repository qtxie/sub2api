package service

import (
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// openAIStripReasoningEffortCredentialKey 存储账号级出站剥离开关：
// credentials["strip_reasoning_effort"] = true 时，出站 CC body 删除 reasoning_effort
// 字段（供不认该字段的上游使用，与 extra_body 注入互补）。
const openAIStripReasoningEffortCredentialKey = "strip_reasoning_effort"

// ShouldStripReasoningEffort 报告账号是否要求出站 CC body 剥离 reasoning_effort。
func (a *Account) ShouldStripReasoningEffort() bool {
	return a != nil && a.Credentials[openAIStripReasoningEffortCredentialKey] == true
}

// NormalizeExtraBodyCredentials 校验 credentials 中的账号级出站附加配置：
//   - credentials["extra_body"]（见 applyAccountExtraBody）：必须是 JSON 对象；
//     空对象放行（accessor 对空对象返回 nil，等价不注入），其余非对象值拒绝——
//     避免静默失效让管理员误以为配置已生效。
//   - credentials["strip_reasoning_effort"]：若提供必须是布尔值。
//
// 供账号创建/更新/批量更新的保存路径调用；credentials 未携带相关字段时为 no-op。
func NormalizeExtraBodyCredentials(credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	raw, ok := credentials[openAIExtraBodyCredentialKey]
	if ok && raw != nil {
		if _, isObj := raw.(map[string]any); !isObj {
			return infraerrors.New(http.StatusBadRequest, "INVALID_EXTRA_BODY",
				"extra_body must be a JSON object")
		}
	}
	if stripRaw, ok := credentials[openAIStripReasoningEffortCredentialKey]; ok && stripRaw != nil {
		if _, isBool := stripRaw.(bool); !isBool {
			return infraerrors.New(http.StatusBadRequest, "INVALID_EXTRA_BODY",
				"strip_reasoning_effort must be a boolean")
		}
	}
	return nil
}

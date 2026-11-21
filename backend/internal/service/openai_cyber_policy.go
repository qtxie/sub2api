package service

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// opsCyberPolicyKey 在 gin context 中携带 cyber_policy 命中标记。
// 由 gateway 服务层在检测到上游 error.code=="cyber_policy" 时设置，
// handler 在 Forward 返回后读取以触发风控记录、邮件与 tokens=0 用量行。
const opsCyberPolicyKey = "ops_cyber_policy"

// errOpenAICyberPolicyForwarded is retained for the compat paths that still
// hand a cyber block straight to the client. HTTP Responses paths now defer
// the block as a recoverable failover error (see GatewayFailureReasonCyberPolicy)
// so same-account model fallback and account failover can be attempted first.
var errOpenAICyberPolicyForwarded = errors.New("openai cyber_policy forwarded to client")

// GatewayFailureReasonCyberPolicy marks an upstream cyber-security rejection
// that is eligible for same-account model fallback and then account failover.
// It stays a distinct reason so account-health and moderation handling can
// tell a content-level block apart from transport or capacity failures.
const GatewayFailureReasonCyberPolicy = GatewayFailureReason("cyber_policy")

// newOpenAICyberPolicyFailoverError defers the client response: the original
// upstream body/message ride along so the handler can still pass the exact
// cyber rejection through once every configured model/account is exhausted.
// Account state is left untouched because a content policy hit says nothing
// about the health of the credential that received it.
func newOpenAICyberPolicyFailoverError(statusCode int, headers http.Header, body []byte, message string) *UpstreamFailoverError {
	if statusCode <= 0 {
		statusCode = http.StatusBadRequest
	}
	var clonedHeaders http.Header
	if headers != nil {
		clonedHeaders = headers.Clone()
	}
	return &UpstreamFailoverError{
		StatusCode:               statusCode,
		ResponseBody:             append([]byte(nil), body...),
		ResponseHeaders:          clonedHeaders,
		Scope:                    GatewayFailureScopeRequest,
		Reason:                   GatewayFailureReasonCyberPolicy,
		NextAccountAction:        NextAccountRetry,
		ClientStatusCode:         statusCode,
		ClientMessage:            strings.TrimSpace(message),
		SafeToFailoverAfterWrite: false,
		AccountStateHandled:      true,
	}
}

// IsCyberPolicyFailoverError reports whether err is a deferred cyber rejection.
func IsCyberPolicyFailoverError(err error) bool {
	var failoverErr *UpstreamFailoverError
	return errors.As(err, &failoverErr) && failoverErr != nil && failoverErr.Reason == GatewayFailureReasonCyberPolicy
}

// CyberPolicyMark 记录一次 cyber_policy 硬阻断的上游证据。
type CyberPolicyMark struct {
	Code           string // 固定 "cyber_policy"
	Message        string // 上游 error.message
	Body           string // 上游 response.failed / 400 原始 body（已截断；未脱敏，ops_error 落库由 sanitizeErrorBodyForStorage、风控日志由 redactContentModerationSecrets 统一脱敏）
	UpstreamStatus int    // 上游 HTTP 状态（流式=200，非流式=400）
	UpstreamInTok  int    // 上游已报 input tokens（如有）
	UpstreamOutTok int    // 上游已报 output tokens（如有）
}

// MarkOpsCyberPolicy 记录 cyber 标记；首个写入生效，后续忽略（同一 turn 只记一次）。
// WS 多轮场景由 handler 在每个 turn 结束后调用 ClearOpsCyberPolicy 重置。
func MarkOpsCyberPolicy(c *gin.Context, mark CyberPolicyMark) {
	if c == nil {
		return
	}
	if GetOpsCyberPolicy(c) != nil {
		return
	}
	mark.Code = "cyber_policy"
	mark.Message = strings.TrimSpace(mark.Message)
	mark.Body = strings.TrimSpace(mark.Body)
	c.Set(opsCyberPolicyKey, &mark)
}

// GetOpsCyberPolicy 返回 cyber 标记，未命中（或已被 Clear）返回 nil。
func GetOpsCyberPolicy(c *gin.Context) *CyberPolicyMark {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(opsCyberPolicyKey); ok {
		if m, ok := v.(*CyberPolicyMark); ok && m != nil {
			return m
		}
	}
	return nil
}

// ClearOpsCyberPolicy 清除 cyber 标记（typed-nil 覆盖；gin context 无并发安全的
// 删除原语，Set 走内部锁，与异步 GetOpsCyberPolicy 不构成 data race）。
// 仅 WS 多轮路径在 turn 收尾调用；HTTP 单请求路径不调用（context 随请求销毁，
// 且中间件 shouldSkipOpsErrorLogForCyber 依赖标记防双写）。
// WS 路径 clear 发生在中间件收尾之前，连接响应状态为 101，不触发中间件 status>=400
// 落库分支，故无双写/漏写。
func ClearOpsCyberPolicy(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(opsCyberPolicyKey, (*CyberPolicyMark)(nil))
}

// detectOpenAICyberPolicy 精确识别 cyber_policy（对齐 codex api_bridge.rs:145 /
// sse/responses.rs:529）。命中返回 (true, "cyber_policy", message)。
func detectOpenAICyberPolicy(payload []byte) (bool, string, string) {
	code := gjson.GetBytes(payload, "error.code").String()
	if code == "" {
		code = gjson.GetBytes(payload, "response.error.code").String()
	}
	if !strings.EqualFold(strings.TrimSpace(code), "cyber_policy") {
		return false, "", ""
	}
	msg := gjson.GetBytes(payload, "error.message").String()
	if msg == "" {
		msg = gjson.GetBytes(payload, "response.error.message").String()
	}
	return true, "cyber_policy", strings.TrimSpace(msg)
}

func markOpenAICyberPolicyEvent(c *gin.Context, payload []byte, upstreamStatus int, usage *OpenAIUsage) bool {
	hit, code, message := detectOpenAICyberPolicy(payload)
	if !hit {
		return false
	}
	mark := CyberPolicyMark{
		Code:           code,
		Message:        message,
		Body:           truncateString(string(payload), 4096),
		UpstreamStatus: upstreamStatus,
	}
	if usage != nil {
		mark.UpstreamInTok = usage.InputTokens
		mark.UpstreamOutTok = usage.OutputTokens
	}
	MarkOpsCyberPolicy(c, mark)
	return true
}

// 上游中转（同为 sub2api，如 input.codes）自身的 cyber 会话屏蔽（其 F5a）命中
// 时的报错文案。中转没有 error.code，只能按 message 识别；中英两段都允许匹配，
// 以免中转版本只回其一。
const (
	upstreamSessionBlockMarkerZh = "该会话已被网络安全策略屏蔽"
	upstreamSessionBlockMarkerEn = "blocked by cyber-security policy, please start a new session"
)

// detectOpenAIUpstreamSessionBlock 识别上游中转的会话屏蔽 400（无 error.code，
// 仅 message 可判）。会话在"该中转"侧被禁，与账号健康无关，换号即可恢复，
// 故不应按普通 400（不可 failover）把错误直接回传客户端。
func detectOpenAIUpstreamSessionBlock(payload []byte) (bool, string) {
	msg := gjson.GetBytes(payload, "error.message").String()
	if msg == "" {
		msg = gjson.GetBytes(payload, "response.error.message").String()
	}
	if msg == "" {
		return false, ""
	}
	if strings.Contains(msg, upstreamSessionBlockMarkerZh) ||
		strings.Contains(strings.ToLower(msg), upstreamSessionBlockMarkerEn) {
		return true, strings.TrimSpace(msg)
	}
	return false, ""
}

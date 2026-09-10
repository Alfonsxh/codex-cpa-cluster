package accountstatus

import "github.com/Alfonsxh/codex-cpa-pool/internal/failover"

// Presentation is the shared Admin/Usage account status. Selectable governs
// manual selection; automatic failover continues to require AccountState.Eligible.
type Presentation struct {
	Code       string `json:"code"`
	Label      string `json:"label"`
	Tone       string `json:"tone"`
	Reason     string `json:"reason"`
	Selectable bool   `json:"selectable"`
}

// Present consumes only the canonical live account state, never a separately
// timed quota or native-runtime read from an individual surface.
func Present(enabled bool, state failover.AccountState, found bool) Presentation {
	if !enabled {
		return Presentation{Code: "disabled", Label: "已停用", Tone: "neutral", Reason: "账号已被管理员停用"}
	}
	if !found {
		return Presentation{Code: "unknown", Label: "状态未知", Tone: "neutral", Reason: "账号运行状态暂不可确认"}
	}
	status := Presentation{Code: "unknown", Label: "状态未知", Tone: "neutral", Reason: "账号运行状态暂不可确认", Selectable: true}
	switch state.Reason {
	case "available":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"available", "可用", "success", "账号当前可用", true
	case "quota_exhausted", "upstream_disallowed":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_exhausted", "额度耗尽", "danger", "账号周额度已耗尽", false
	case "account_disabled":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"disabled", "已停用", "neutral", "账号已被管理员停用", false
	case "container_not_running":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"stopped", "已停止", "danger", "CPA 服务未运行", false
	case "oauth_missing":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"auth_missing", "未授权", "danger", "OAuth 尚未授权", false
	case "credential_unavailable":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"credential_unavailable", "凭据不可用", "danger", "OAuth 凭据已失效，需要重新授权", false
	case "transient_cooldown":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"transient_cooldown", "临时冷却", "warning", "上游请求临时失败，CPA 正在等待凭据冷却恢复", true
	case "rate_limited":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"rate_limited", "限流中", "warning", "账号近期出现 429，仍可选择并稍后重试", true
	case "degraded":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"degraded", "近期异常", "warning", "账号近期出现请求异常", true
	case "runtime_unknown":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"unknown", "状态未知", "neutral", "CPA 原生状态暂不可查询", true
	case "reserve_reached":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_warning", "额度预留", "warning", "账号已达到预留额度", true
	case "quota_stale":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"unknown", "状态未知", "neutral", "账号实时状态暂不可确认", true
	case "quota_unavailable":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_unknown", "额度未知", "neutral", "额度状态暂不可确认", true
	}
	if state.Exhausted {
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_exhausted", "额度耗尽", "danger", "账号周额度已耗尽", false
	}
	if status.Code == "available" && state.RemainingPercent != nil && *state.RemainingPercent <= 10 {
		status.Code, status.Label, status.Tone, status.Reason = "quota_warning", "注意额度", "warning", "周额度剩余不高于 10%"
	}
	return status
}

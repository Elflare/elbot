package security

import (
	"strings"

	"elbot/internal/contextinfo"
)

type RiskLevel string

const (
	RiskSafe     RiskLevel = "safe"
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// Permission resolves actor roles and applies tool access and confirmation limits.
type Permission struct {
	UserMaxToolRisk       RiskLevel                  // Highest tool risk allowed for regular users.
	SuperadminConfirmRisk RiskLevel                  // Risk threshold requiring superadmin confirmation.
	Superadmins           map[string]map[string]bool // Platform name -> native user ID -> membership.
}

// ToolPermissionResult 描述本次工具调用的权限与确认要求。
type ToolPermissionResult struct {
	Allowed           bool   // 是否具备执行权限；为 false 时不得进入确认或执行。
	NeedsConfirmation bool   // 已有执行权限，但仍需取得用户确认。
	Reason            string // 拒绝或自动确认的策略原因，供调用方组织提示与审计。
}

// CheckTool 统一判断工具权限与确认要求；backgroundKind 仅在后台沙盒中填写。
// 场景和风险由调用方提供，本方法不读取业务组件、不交互也不发布日志。
func (p *Permission) CheckTool(actor contextinfo.Actor, name string, risk RiskLevel, ownerScoped, superadminOnly bool, backgroundKind string) ToolPermissionResult {
	if superadminOnly && actor.Role != contextinfo.RoleSuperadmin {
		return ToolPermissionResult{Reason: "tool_requires_superadmin"}
	}
	if !p.CanUseTool(actor, risk, ownerScoped) {
		return ToolPermissionResult{Reason: "tool_risk_above_allowed_level"}
	}
	if name == "shell" && (backgroundKind == "cron" || backgroundKind == "elnis") {
		if risk == RiskCritical {
			return ToolPermissionResult{Reason: "background_shell_rejected"}
		}
		return ToolPermissionResult{Allowed: true, Reason: "background_shell_auto_confirmed"}
	}
	return ToolPermissionResult{Allowed: true, NeedsConfirmation: p.NeedsToolConfirmation(actor, risk)}
}

func NewPermission(userMaxToolRisk, superadminConfirmRisk string, superadmins map[string][]string) *Permission {
	p := &Permission{
		UserMaxToolRisk:       ParseRisk(userMaxToolRisk, RiskLow),
		SuperadminConfirmRisk: ParseRisk(superadminConfirmRisk, RiskHigh),
		Superadmins:           map[string]map[string]bool{},
	}
	for platform, ids := range superadmins {
		platform = normalize(platform)
		if platform == "" {
			continue
		}
		if p.Superadmins[platform] == nil {
			p.Superadmins[platform] = map[string]bool{}
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" {
				p.Superadmins[platform][id] = true
			}
		}
	}
	return p
}

func DefaultPermission() *Permission {
	return NewPermission("low", "high", map[string][]string{"cli": {"local"}})
}

func ParseRisk(value string, fallback RiskLevel) RiskLevel {
	switch RiskLevel(normalize(value)) {
	case RiskSafe, RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return RiskLevel(normalize(value))
	default:
		return fallback
	}
}

func (p *Permission) Actor(id, platform, platformUserID, displayName string) contextinfo.Actor {
	platform = normalize(platform)
	platformUserID = canonicalPlatformUserID(platform, platformUserID)
	if platformUserID == "" {
		platformUserID = canonicalPlatformUserID(platform, id)
	}
	id = ActorID(platform, platformUserID)
	role := contextinfo.RoleUser
	if p != nil && p.IsSuperadmin(platform, platformUserID) {
		role = contextinfo.RoleSuperadmin
	}
	return contextinfo.Actor{ID: id, Platform: platform, PlatformUserID: platformUserID, DisplayName: displayName, Role: role, GroupRole: contextinfo.GroupRoleUnknown}
}

func ParseGroupRole(value string) contextinfo.GroupRole {
	switch normalize(value) {
	case "owner", "group_owner", "creator":
		return contextinfo.GroupRoleOwner
	case "admin", "administrator", "group_admin":
		return contextinfo.GroupRoleAdmin
	case "member", "user", "group_member":
		return contextinfo.GroupRoleMember
	case "unknown", "":
		return contextinfo.GroupRoleUnknown
	default:
		return contextinfo.GroupRoleUnknown
	}
}

func ActorID(platform, platformUserID string) string {
	platform = normalize(platform)
	platformUserID = canonicalPlatformUserID(platform, platformUserID)
	if platform == "" {
		platform = "unknown"
	}
	if platformUserID == "" {
		platformUserID = "unknown"
	}
	return platform + ":" + platformUserID
}

func canonicalPlatformUserID(platform, value string) string {
	value = strings.TrimSpace(value)
	prefix := normalize(platform) + ":"
	if prefix != ":" && strings.HasPrefix(value, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	return value
}

func (p *Permission) IsSuperadmin(platform, platformUserID string) bool {
	if p == nil {
		return false
	}
	ids := p.Superadmins[normalize(platform)]
	return ids != nil && ids[strings.TrimSpace(platformUserID)]
}

func (p *Permission) CanUseTool(actor contextinfo.Actor, risk RiskLevel, ownerScoped bool) bool {
	if actor.Role == contextinfo.RoleSuperadmin {
		return true
	}
	if ownerScoped {
		return true
	}
	maxRisk := RiskLow
	if p != nil {
		maxRisk = p.UserMaxToolRisk
	}
	return CompareRisk(risk, maxRisk) <= 0
}

func (p *Permission) NeedsToolConfirmation(actor contextinfo.Actor, risk RiskLevel) bool {
	threshold := RiskHigh
	if actor.Role == contextinfo.RoleSuperadmin && p != nil {
		threshold = p.SuperadminConfirmRisk
	}
	return CompareRisk(risk, threshold) >= 0
}

func CompareRisk(left, right RiskLevel) int {
	return riskRank(left) - riskRank(right)
}

func riskRank(risk RiskLevel) int {
	switch risk {
	case RiskSafe:
		return 0
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	case RiskHigh:
		return 3
	case RiskCritical:
		return 4
	default:
		return 3
	}
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

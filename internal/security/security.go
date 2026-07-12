package security

import "strings"

type Role string

const (
	RoleSuperadmin Role = "superadmin"
	RoleUser       Role = "user"
)

type GroupRole string

const (
	GroupRoleUnknown GroupRole = "unknown"
	GroupRoleOwner   GroupRole = "owner"
	GroupRoleAdmin   GroupRole = "admin"
	GroupRoleMember  GroupRole = "member"
)

type RiskLevel string

const (
	RiskSafe     RiskLevel = "safe"
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

type Actor struct {
	ID             string
	Platform       string
	PlatformUserID string
	Nickname       string
	GroupCard      string
	DisplayName    string
	Role           Role
	GroupRole      GroupRole
}

type Policy struct {
	UserMaxToolRisk       RiskLevel
	SuperadminConfirmRisk RiskLevel
	Superadmins           map[string]map[string]bool
}

func NewPolicy(userMaxToolRisk, superadminConfirmRisk string, superadmins map[string][]string) *Policy {
	p := &Policy{
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

func DefaultPolicy() *Policy {
	return NewPolicy("low", "high", map[string][]string{"cli": {"local"}})
}

func ParseRisk(value string, fallback RiskLevel) RiskLevel {
	switch RiskLevel(normalize(value)) {
	case RiskSafe, RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return RiskLevel(normalize(value))
	default:
		return fallback
	}
}

func (p *Policy) Actor(id, platform, platformUserID, displayName string) Actor {
	platform = normalize(platform)
	platformUserID = canonicalPlatformUserID(platform, platformUserID)
	if platformUserID == "" {
		platformUserID = canonicalPlatformUserID(platform, id)
	}
	id = ActorID(platform, platformUserID)
	role := RoleUser
	if p != nil && p.IsSuperadmin(platform, platformUserID) {
		role = RoleSuperadmin
	}
	return Actor{ID: id, Platform: platform, PlatformUserID: platformUserID, DisplayName: displayName, Role: role, GroupRole: GroupRoleUnknown}
}

func ParseGroupRole(value string) GroupRole {
	switch normalize(value) {
	case "owner", "group_owner", "creator":
		return GroupRoleOwner
	case "admin", "administrator", "group_admin":
		return GroupRoleAdmin
	case "member", "user", "group_member":
		return GroupRoleMember
	case "unknown", "":
		return GroupRoleUnknown
	default:
		return GroupRoleUnknown
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

func (p *Policy) IsSuperadmin(platform, platformUserID string) bool {
	if p == nil {
		return false
	}
	ids := p.Superadmins[normalize(platform)]
	return ids != nil && ids[strings.TrimSpace(platformUserID)]
}

func (p *Policy) CanUseTool(actor Actor, risk RiskLevel, ownerScoped bool) bool {
	if actor.Role == RoleSuperadmin {
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

func (p *Policy) NeedsToolConfirmation(actor Actor, risk RiskLevel) bool {
	if actor.Role != RoleSuperadmin {
		return false
	}
	threshold := RiskHigh
	if p != nil {
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

package contextinfo

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

// Actor is the resolved ElBot identity. Only the identity resolver establishes
// it; consumers still apply their security policy to these facts.
type Actor struct {
	ID             string // 存储关联标识，不用于反向解析身份。
	Platform       string // 来源平台的规范名称。
	PlatformUserID string // 平台原生用户 ID，权限判断直接使用。
	Nickname       string
	GroupCard      string
	DisplayName    string
	Role           Role
	GroupRole      GroupRole
}

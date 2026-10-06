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
	ID             string
	Platform       string
	PlatformUserID string
	Nickname       string
	GroupCard      string
	DisplayName    string
	Role           Role
	GroupRole      GroupRole
}

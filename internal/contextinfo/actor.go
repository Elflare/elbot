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

// ActorIdentity 保存可比较的身份事实；只使用平台和原生 ID，展示名不参与比较。
type ActorIdentity struct {
	Platform string `json:"platform,omitempty" toml:"platform,omitempty"` // 平台用户所属平台。
	ID       string `json:"id,omitempty" toml:"id,omitempty"`             // 原生用户 ID；Elwisp 使用协议名称。
}

// Actor is the resolved ElBot identity. Only the identity resolver establishes
// it; consumers still apply their security policy to these facts.
type Actor struct {
	ActorIdentity
	Nickname  string    `json:"nickname,omitempty"`   // 平台昵称。
	GroupCard string    `json:"group_card,omitempty"` // 平台群名片。
	Name      string    `json:"name"`                 // 平台选定的展示名，不参与身份比较。
	Role      Role      `json:"role,omitempty"`
	GroupRole GroupRole `json:"group_role,omitempty"`
}

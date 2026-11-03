// Package contextinfo owns public, per-operation facts. It has no dependency
// on business services, permissions, delivery or concrete platform adapters.
package contextinfo

type ChatType string

const (
	ChatUnknown ChatType = ""
	ChatPrivate ChatType = "private"
	ChatGroup   ChatType = "group"
	ChatChannel ChatType = "channel"
)

type Source struct {
	Platform string   `json:"platform"`  // 聊天所属平台。
	ChatType ChatType `json:"chat_type"` // 私聊、群聊或频道。
	ChatID   string   `json:"id"`        // 平台内原生聊天 ID，不包含类型前缀。
	Name     string   `json:"name"`      // 聊天展示名称，不参与身份判断。
}

// Identity describes the platform sender before ElBot authorization resolves
// an Actor. Permission is resolved from the platform and its native user ID.
type Identity struct {
	ID        string // 平台原生用户 ID。
	Nickname  string // 平台昵称。
	GroupCard string // 群名片。
	Name      string // 平台选择的展示名，不参与身份比较。
}

type Conversation struct {
	Source            Source
	Identity          Identity
	PlatformMessageID string
	ReplyToMessageID  string
	ReplyToSenderID   string
	// Platforms own this immutable extension. Resource references retain their
	// platform-managed lifetime and are never persistent delivery addresses.
	PlatformData any `json:"-"`
}

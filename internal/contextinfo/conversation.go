// Package contextinfo owns public, per-operation facts. It has no dependency
// on business services, permissions, delivery or concrete platform adapters.
package contextinfo

type ConversationKind string

const (
	ConversationUnknown ConversationKind = "unknown"
	ConversationPrivate ConversationKind = "private"
	ConversationGroup   ConversationKind = "group"
	ConversationChannel ConversationKind = "channel"
)

type Source struct {
	Platform         string
	ScopeID          string
	ConversationKind ConversationKind
	ConversationID   string
}

// Identity describes the platform sender before ElBot authorization resolves
// an Actor. ActorID is an association supplied by the source, not permission.
type Identity struct {
	ActorID        string
	PlatformUserID string
	Nickname       string
	GroupCard      string
	DisplayName    string
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

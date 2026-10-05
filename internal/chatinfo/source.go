// Package chatinfo carries per-message conversation, sender and reply facts.
// Platforms own opaque extensions. It has no dependency on permissions,
// sessions, delivery or concrete platform implementations.
package chatinfo

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

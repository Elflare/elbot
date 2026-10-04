// Package chatinfo carries immutable, per-message conversation and sender facts.
// It deliberately has no dependency on permissions, sessions or delivery.
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

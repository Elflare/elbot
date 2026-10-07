package llm

// Origin records the configured source of conversation material. It is a value
// snapshot, not a client or a lookup of the current configuration. A migrated
// Chat session has only APIType until its next admitted dialogue operation.
type Origin struct {
	APIType  APIType `json:"protocol"`
	Provider string  `json:"provider,omitempty"`
	BaseURL  string  `json:"base_url,omitempty"`
}

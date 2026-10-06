package llm

// ProtocolID identifies a wire protocol, independently of a session's mode.
type ProtocolID string

const (
	ProtocolChat     ProtocolID = "chat"
	ProtocolResponse ProtocolID = "response"
)

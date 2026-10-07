package llm

// APIType identifies the model API, independently of a session mode.
type APIType string

const (
	APITypeChat     APIType = "chat"
	APITypeResponse APIType = "response"
)

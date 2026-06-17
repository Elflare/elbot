package elnis

import "time"

const (
	ModeRecord = "record"
	ModeDirect = "direct"
	ModeLLM    = "llm"

	StatusAccepted    = "accepted"
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusDuplicate   = "duplicate"
	StatusUnsupported = "unsupported"
)

type Request struct {
	Version       string         `json:"version"`
	Elwisp        Elwisp         `json:"elwisp"`
	Source        string         `json:"source"`
	ID            string         `json:"id"`
	CreatedAt     string         `json:"created_at"`
	Mode          string         `json:"mode"`
	Title         string         `json:"title"`
	Format        string         `json:"format"`
	Content       string         `json:"content"`
	ModelSlot     string         `json:"model_slot"`
	ToolListNames []string       `json:"tool_list_names"`
	Targets       Targets        `json:"targets"`
	Meta          map[string]any `json:"meta"`
}

type Elwisp struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type Targets struct {
	Platforms   []string `json:"platforms"`
	Superadmins bool     `json:"superadmins"`
}

type Response struct {
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
	EventKey  string `json:"event_key"`
	Mode      string `json:"mode"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type Event struct {
	Request          Request
	TokenName        string
	EventKey         string
	ContentHash      string
	TagsJSON         string
	RequestedTargets string
	ResolvedTargets  string
	CreatedAt        time.Time
	ReceivedAt       time.Time
}

package elnis

import (
	"time"

	"elbot/internal/toolrun"
)

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
	Version       string                          `json:"version"`
	Elwisp        Elwisp                          `json:"elwisp"`
	Source        string                          `json:"source"`
	ID            string                          `json:"id"`
	CreatedAt     string                          `json:"created_at"`
	Mode          string                          `json:"mode"`
	Title         string                          `json:"title"`
	Format        string                          `json:"format"`
	Content       string                          `json:"content"`
	ModelSlot     string                          `json:"model_slot"`
	ToolListNames []string                        `json:"tool_list_names"`
	Tools         []toolrun.ELwispToolDeclaration `json:"tools"`
	Segments      []Segment                       `json:"segments,omitempty"`
	Targets       []Target                        `json:"targets"`
	Meta          map[string]any                  `json:"meta"`
}

type Elwisp struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// SegmentKind is the type of an Elvena message segment.
type SegmentKind string

const (
	SegmentKindText  SegmentKind = "text"
	SegmentKindImage SegmentKind = "image"
	SegmentKindFile  SegmentKind = "file"
)

// Segment is a typed content segment in an Elvena event.
type Segment struct {
	Kind     SegmentKind `json:"kind"`
	Text     string      `json:"text,omitempty"`
	URL      string      `json:"url,omitempty"`
	Name     string      `json:"name,omitempty"`
	MIMEType string      `json:"mime_type,omitempty"`
}

type Target struct {
	Platform string `json:"platform" toml:"platform"`
	Type     string `json:"type,omitempty" toml:"type"`
	ID       string `json:"id,omitempty" toml:"id"`
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
	ToolDeclarations string
	ToolHash         string
	TagsJSON         string
	RequestedTargets string
	ResolvedTargets  string
	SegmentPaths     map[string]string
	CreatedAt        time.Time
	ReceivedAt       time.Time
}

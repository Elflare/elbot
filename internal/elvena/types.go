// types.go 定义公共请求、响应、片段、目标与平台调用声明。

package elvena

import "elbot/internal/tool"

const (
	VersionV2 = "elvena.v2"
	VersionV3 = "elvena.v3"

	ModeRecord = "record"
	ModeDirect = "direct"
	ModeLLM    = "llm"

	StatusAccepted    = "accepted"
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusResultReady = "result_ready"
	StatusDelivering  = "delivering"
	StatusCompleted   = "completed"
	StatusTakenOver   = "taken_over"
	StatusFailed      = "failed"
	StatusDuplicate   = "duplicate"
	StatusUnsupported = "unsupported"
)

type Request struct {
	Version       string                       `json:"version"`
	Elwisp        Elwisp                       `json:"elwisp"`
	Source        string                       `json:"source"`
	ID            string                       `json:"id"`
	CreatedAt     string                       `json:"created_at"`
	Mode          string                       `json:"mode"`
	Title         string                       `json:"title"`
	Format        string                       `json:"format"`
	Content       string                       `json:"content"`
	ModelSlot     string                       `json:"model_slot"`
	ToolListNames []string                     `json:"tool_list_names"`
	Tools         []tool.ELwispToolDeclaration `json:"tools"`
	Segments      []Segment                    `json:"segments,omitempty"`
	Targets       []Target                     `json:"targets"`
	Calls         []Call                       `json:"calls,omitempty"`
	Meta          map[string]any               `json:"meta"`
}

type Elwisp struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type SegmentKind string

const (
	SegmentKindText  SegmentKind = "text"
	SegmentKindImage SegmentKind = "image"
	SegmentKindFile  SegmentKind = "file"
)

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

// Call 描述平台原生 API 请求，平台选择与执行归接收方。
type Call struct {
	Platform string         `json:"platform,omitempty"` // 平台名称；省略时由接收方选择。
	API      string         `json:"api,omitempty"`      // 平台原生接口名。
	Params   map[string]any `json:"params,omitempty"`   // 原生接口参数，由平台解释。
}

type Response struct {
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
	EventKey  string `json:"event_key"`
	Mode      string `json:"mode"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

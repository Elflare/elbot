package share

import toolshare "elbot/internal/tool/share"

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
)

type Kind string

const (
	KindCron  Kind = "cron"
	KindElnis Kind = "elnis"
)

type Runner interface {
	RunBackground(ctx context.Context, req RunRequest) (RunResult, error)
}

type RunRequest struct {
	// NeedsElyphRules declares a requirement of the task prompt, not session state.
	NeedsElyphRules bool
	Kind            Kind
	Name            string
	Title           string
	Platform        string
	Actor           contextinfo.Actor
	ScopeID         string
	SessionID       string
	ModelProvider   string
	Model           string
	PromptSegments  []llm.MessageSegment
	Prompt          string
	RetryPrompt     string
	// Tool inputs initialize new sessions only; continuations inherit committed state.
	// AllowedToolNames optionally restricts root tools (nil means unrestricted).
	ToolListNames    []string
	AllowedToolNames []string
	ELwispTools      []toolshare.ELwispToolDeclaration // 已授权并补齐模型侧名称、事件标识的外部声明。
	SandboxSubdir    string
	Metadata         map[string]string
}

type RunResult struct {
	RunID     string
	TakenOver bool
	Outcome   string
	SessionID string
	MessageID string
	Text      string
	Parsed    JSONResult
	ParseErr  error
}

type JSONResult struct {
	Completed      bool                 `json:"completed"`
	NeedReport     bool                 `json:"need_report"`
	ReportSegments []llm.MessageSegment `json:"report_segments,omitempty"`
	Report         string               `json:"report"`
}

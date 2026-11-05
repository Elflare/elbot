package share

import "elbot/internal/tool"

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
)

type Kind string

const (
	KindCron  Kind = "cron"
	KindElnis Kind = "elnis"
)

type Runner interface {
	RunBackground(ctx context.Context, req RunRequest) (context.Context, RunResult, error)
}

type RunRequest struct {
	TargetPlatforms []string // 创建时解析出的有效投递平台，供 Session 保存身份快照。
	// NeedsElyphRules declares a requirement of the task prompt, not session state.
	NeedsElyphRules bool
	TaskType        Kind
	TaskID          string
	Name            string
	Actor           contextinfo.Actor
	SessionID       string
	ModelProvider   string
	Model           string
	PromptSegments  []llm.MessageSegment
	Prompt          string
	// Tool inputs initialize new sessions only; continuations inherit committed state.
	// AllowedToolNames optionally restricts root tools (nil means unrestricted).
	ToolListNames    []string
	AllowedToolNames []string
	ELwispTools      []tool.ELwispToolDeclaration // 已授权并补齐模型侧名称、事件标识的外部声明。
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
}

type JSONResult struct {
	Completed      bool              `json:"completed"`
	NeedReport     bool              `json:"need_report"`
	ReportSegments []delivery.Output `json:"report_segments,omitempty"` // 已分类的附件；路径为任务目录内的相对路径。
	Report         string            `json:"report"`
}

package background

import (
	"context"
	"elbot/internal/llm"

	"elbot/internal/security"
	"elbot/internal/toolrun"
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
	Kind           Kind
	Name           string
	Title          string
	Platform       string
	Actor          security.Actor
	ScopeID        string
	SessionID      string
	ModelProvider  string
	Model          string
	PromptSegments []llm.MessageSegment
	Prompt         string
	RetryPrompt    string
	ToolListNames  []string
	CachedTools    []toolrun.CachedTool
	SandboxSubdir  string
	Metadata       map[string]string
}

type RunResult struct {
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

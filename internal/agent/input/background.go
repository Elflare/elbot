package input

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

// BackgroundHandler prepares task input and delegates execution to the existing coordinator.
type BackgroundHandler struct {
	BackgroundHandlerDeps // Borrowed preparation capabilities, wired by the Agent root.
}

func (r *BackgroundHandler) RunBackground(ctx context.Context, req BackgroundRequest) (context.Context, turn.Result) {
	actor := req.Actor
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.Mode, facts.TaskType, facts.TaskID = storage.SessionModeBackground, req.TaskType, req.TaskID
	ctx = contextinfo.WithExecution(ctx, facts)
	ctx = contextinfo.WithoutConversation(ctx)
	previous, _ := agentshare.MessageContextFrom(ctx)
	ctx = agentshare.WithMessageContext(ctx, agentshare.MessageContext{RawText: req.Prompt, Segments: backgroundPromptSegments(req.PromptSegments), RestoreMessageContext: previous.RestoreMessageContext})
	ctx = contextinfo.WithActor(ctx, actor)

	scope, _ := workspace.PathScopeFromContext(ctx)

	if req.ModelProvider != "" || req.Model != "" {
		ctx = modelmgr.WithSelectionOverride(ctx, config.ModelSelection{Provider: req.ModelProvider, Model: req.Model})
	}

	sessionScope := session.Scope{Owner: actor.ActorIdentity, Platform: actor.Platform, TaskType: req.TaskType, TaskID: req.TaskID}
	bgSession, err := r.Sessions.PrepareBackground(ctx, sessionScope, session.BackgroundRequest{TargetPlatforms: req.TargetPlatforms, SessionID: req.SessionID, TaskType: string(req.TaskType), TaskID: req.TaskID, Name: req.Name, Metadata: req.Metadata})
	if err != nil {
		if errors.Is(err, session.ErrForegroundSession) {
			return ctx, turn.Result{SessionID: req.SessionID, TakenOver: true, Outcome: "taken_over"}
		}
		return ctx, turn.Result{Err: err}
	}
	facts.SessionID = bgSession.ID
	ctx = contextinfo.WithExecution(ctx, facts)
	if err := r.EnsureWorkspaceDir(ctx, bgSession.ID, scope.Dir); err != nil {
		return ctx, turn.Result{SessionID: bgSession.ID, Err: err}
	}
	var preloaded backgroundPreloadResult
	if req.SessionID == "" {
		ctx, err = r.Execution.ModelContext(ctx, bgSession)
		if err != nil {
			return ctx, turn.Result{SessionID: bgSession.ID, Err: err}
		}
		preloaded = r.preloadBackgroundResources(ctx, bgSession, req.ToolListNames, req.ELwispTools, req.AllowedToolNames)
	}
	if preloaded.Err != nil {
		return ctx, turn.Result{SessionID: bgSession.ID, Err: preloaded.Err}
	}
	record := globalevents.LogRecord{
		Category:     globalevents.LogAudit,
		Level:        slog.LevelInfo,
		Module:       "agent",
		Type:         globalevents.ContentJSON,
		ContentLimit: 4096,
		ResultStatus: globalevents.ResultSucceeded,
	}
	facts, _ = contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = bgSession.ID
	if len(preloaded.Tools) > 0 {
		content, _ := json.Marshal(struct {
			TaskType string   `json:"task_type"`
			TaskID   string   `json:"task_id"`
			Tools    []string `json:"tools"`
		}{req.TaskType, req.TaskID, preloaded.Tools})
		record.EventName, record.Content = "background_tool_preloaded", string(content)
		agentshare.EmitAgentLog(ctx, facts, record)
	}
	if len(preloaded.Skills) > 0 {
		content, _ := json.Marshal(struct {
			TaskType string   `json:"task_type"`
			TaskID   string   `json:"task_id"`
			Skills   []string `json:"skills"`
		}{req.TaskType, req.TaskID, preloaded.Skills})
		record.EventName, record.Content = "background_skill_preloaded", string(content)
		agentshare.EmitAgentLog(ctx, facts, record)
	}
	prompt := backgroundPromptWithSkills(req.Prompt, preloaded.SkillPrompt)
	input := agentshare.InboundTurnInput(ctx, prompt)
	if strings.TrimSpace(preloaded.SkillPrompt) != "" {
		input.Segments = agentshare.ReplaceInboundTextSegments(ctx, prompt)
		input.Text = llm.SegmentsTextOnly(input.Segments)
	}
	input.NeedsElyphRules = req.NeedsElyphRules || preloaded.NeedsElyphRules
	ctx = agentshare.WithInboundTurnInput(ctx, input)
	return r.Execution.RunBackground(ctx, bgSession, prompt)
}

func backgroundPromptSegments(segments []llm.MessageSegment) []agentshare.MessageSegment {
	out := make([]agentshare.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			out = append(out, agentshare.MessageSegment{Type: "text", Text: segment.Text, Media: media.PlatformMedia{Type: "text", MIMEType: segment.MIMEType, Name: segment.Name}})
		case llm.SegmentImage:
			out = append(out, agentshare.MessageSegment{Type: "image", Text: segment.Text, Media: media.PlatformMedia{Type: "image", URL: segment.URL, MediaID: segment.MediaID, MIMEType: segment.MIMEType, Name: segment.Name}})
		case llm.SegmentFile:
			out = append(out, agentshare.MessageSegment{Type: "file", Text: segment.Text, Media: media.PlatformMedia{Type: "file", URL: segment.URL, MediaID: segment.MediaID, MIMEType: segment.MIMEType, Name: segment.Name}})
		}
	}
	return out
}

type backgroundPreloadResult struct {
	NeedsElyphRules bool     // Prepared material requires the shared ELyph instructions.
	Err             error    // Preparation or commit failure prevents execution.
	Tools           []string // Tool declarations committed to the session.
	Skills          []string // Skill names included in the prepared material.
	SkillPrompt     string   // Prepared instructions prepended to the first task input.
}

func (r *BackgroundHandler) preloadBackgroundResources(ctx context.Context, row *storage.Session, names []string, declarations []tool.ELwispToolDeclaration, allowed []string) backgroundPreloadResult {
	prepared, err := r.Preloader.PrepareBackground(ctx, tool.BackgroundPreloadRequest{
		SessionID: row.ID, Names: names, AllowedToolNames: allowed, ELwispTools: declarations,
	})
	if err != nil {
		return backgroundPreloadResult{Err: err}
	}
	committed, err := agentshare.CommitToolState(ctx, r.ToolState, row, prepared.Update)
	if err != nil {
		return backgroundPreloadResult{Err: err}
	}
	return backgroundPreloadResult{Tools: committed.Injected, Skills: prepared.Skills, SkillPrompt: prepared.SkillPrompt, NeedsElyphRules: prepared.NeedsElyphRules}
}

func backgroundPromptWithSkills(prompt, skillPrompt string) string {
	skillPrompt = strings.TrimSpace(skillPrompt)
	if skillPrompt == "" {
		return prompt
	}
	return "[系统预加载资源]\n\n以下工具使用说明与 Skill 文档已由系统预加载。可调用能力以本次请求注入的 top-level tool schema 为准。\n\n" + skillPrompt + "\n\n[后台任务]\n\n" + strings.TrimSpace(prompt)
}

// NewBackgroundHandler receives only the preparation and execution capabilities it consumes.
func NewBackgroundHandler(deps BackgroundHandlerDeps) *BackgroundHandler {
	return &BackgroundHandler{BackgroundHandlerDeps: deps}
}

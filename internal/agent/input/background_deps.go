package input

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

// BackgroundRequest is the input component's task preparation data.
// Workspace scope and background source are supplied by the caller's context.
type BackgroundRequest struct {
	TargetPlatforms  []string                     // 创建时有效的投递平台，不替代任务归属平台。
	TaskType         string                       // 后台任务来源。
	TaskID           string                       // 来源内的任务标识。
	Name             string                       // 新会话标题。
	Actor            contextinfo.Actor            // 执行身份。
	SessionID        string                       // 续跑会话；空值创建新会话。
	ModelProvider    string                       // 本次模型提供方覆盖。
	Model            string                       // 本次模型名称覆盖。
	Prompt           string                       // 本次输入文本。
	PromptSegments   []llm.MessageSegment         // 本次输入的有序多模态片段。
	NeedsElyphRules  bool                         // 输入材料是否要求 ELyph 通用规则。
	ToolListNames    []string                     // 初始化时选择的工具或技能。
	AllowedToolNames []string                     // 本地根工具限制；nil 不额外限制，空切片禁止。
	ELwispTools      []tool.ELwispToolDeclaration // 已授权的远端工具声明。
	Metadata         map[string]string            // 写入任务会话的来源元数据。
}

// BackgroundHandlerDeps contains only capabilities needed to prepare a task turn.
type BackgroundHandlerDeps struct {
	Sessions interface {
		PrepareBackground(context.Context, session.Scope, session.BackgroundRequest) (*storage.Session, error)
	} // Creates or resumes a task session without activating a foreground binding.
	Identity interface {
		IsCLI(context.Context) bool
	} // 根据当前请求来源识别 CLI 平台。
	WithPermission func(context.Context) context.Context // Attaches the permission policy supplied by the Agent root.
	Execution      interface {
		ModelContext(context.Context, *storage.Session) (context.Context, error)
		RunBackground(context.Context, *storage.Session, string) turn.Result
	} // Resolves model context and executes through the existing coordinator.
	Preloader interface {
		PrepareBackground(context.Context, tool.BackgroundPreloadRequest) (tool.BackgroundPreload, error)
	} // Prepares declarations and Skill content for a new task session.
	ToolState interface {
		Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, error)
	} // Commits the prepared declarations to the owning session.
	EnsureWorkspaceDir func(context.Context, string, string) error // Initializes the session directory without replacing a concurrent foreground choice.
}

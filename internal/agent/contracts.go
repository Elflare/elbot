// Agent 对外的回调、请求与结果契约。
package agent

import (
	"context"
	"encoding/json"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/tool"
)

// MessageRequest 是一次平台输入；会话与来源事实通过 contextinfo 提供。
type MessageRequest struct {
	Text                  string                                                                     // 交给 Agent 处理的正文。
	RawText               string                                                                     // 平台原始正文。
	GroupRole             contextinfo.GroupRole                                                      // 平台群身份，用于行为者权限解析。
	BufferAssistantOutput bool                                                                       // 是否等待整段助手输出后发送。
	ForkFromMessageID     string                                                                     // 可选分叉来源。
	ResumeSessionID       string                                                                     // 可选恢复会话。
	Segments              []MessageSegment                                                           // 当前输入片段。
	ContextText           string                                                                     // 引用与当前输入组合的正文。
	ContextSegments       []MessageSegment                                                           // 有序上下文片段。
	Reply                 ReplyContext                                                               // 原始引用事实。
	Bot                   Identity                                                                   // 当前平台机器人身份。
	Mentions              []Mention                                                                  // 输入中的提及事实。
	TriggerKeywords       []string                                                                   // 此平台的唤醒词。
	ResolveMedia          func(context.Context, media.PlatformMedia, int64) (delivery.Source, error) // 延迟解析媒体来源，不在输入转换时下载。
}

type MessageSegment struct {
	Type   string              // text、image、file 或 at。
	Text   string              // 正文或附件说明。
	UserID string              // 被提及的用户标识。
	Media  media.PlatformMedia // 媒体来源描述。
}

type ReplyContext struct {
	MessageID  string           // 引用的平台消息标识。
	SenderID   string           // 引用发送者标识。
	SenderName string           // 引用发送者展示名。
	Text       string           // 引用正文。
	Segments   []MessageSegment // 引用原始片段，供媒体关联使用。
}

type Identity struct {
	UserID   string // 平台用户标识。
	Username string // 平台用户名。
}

type Mention struct {
	UserID   string // 被提及的平台用户标识。
	Username string // 被提及的用户名。
	Text     string // 原始提及文字。
}

// Points 是提供方注册的回调集合，契约定义见 share。
type Points = agentshare.Points

// Facts 是回调使用的 Agent 事实快照。
type Facts = agentshare.Facts

type Session = agentshare.Session
type Message = agentshare.Message
type PlatformMessageReceivedResult = agentshare.PlatformMessageReceivedResult
type LLMInput = agentshare.LLMInput
type LLMInputResult = agentshare.LLMInputResult
type LLMResponse = agentshare.LLMResponse
type LLMResponseResult = agentshare.LLMResponseResult
type ToolResult = agentshare.ToolResult

type StatusChangedEvent struct {
	Snapshot     runtimestatus.Snapshot    // Committed execution state, independent of presentation.
	Version      uint64                    // Monotonic publication order within the recorder, not a binding version.
	Execution    contextinfo.Execution     // Identity of the execution that published the state, including terminal states.
	Conversation *contextinfo.Conversation // Original presentation destination; nil when no conversation exists.
	Binding      *BindingSnapshot          // Activation associated with this state; nil when unbound.
}

// BindingSnapshot describes an activation without exposing its internal handle.
type BindingSnapshot struct {
	Scope     session.Scope // Conversation and actor scope of the activation.
	SessionID string        // Persisted session selected by this activation.
	Version   uint64        // Service-local version assigned when this activation was established.
}

// BindingChangedEvent contains independently copied facts for one subscriber.
// At least one of Old and New is present.
type BindingChangedEvent struct {
	Version uint64           // Service-local committed change version; unbinding also advances it.
	Old     *BindingSnapshot // Previous activation; nil when none existed.
	New     *BindingSnapshot // New activation; nil when the scope becomes unbound.
}

// BackgroundRequest 描述后台执行输入；调用方在 context 中提供已准备的非空工作目录范围与后台来源。
// 工具声明仅初始化新会话，续跑沿用会话中已提交的状态。
type BackgroundRequest struct {
	TargetPlatforms  []string                     // Background 解析出的有效投递平台，用于新会话的身份快照。
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

// BackgroundResult 只返回执行事实；报告解析和重试策略归 Background。
type BackgroundResult struct {
	RunID     string // 本次执行标识。
	SessionID string // 实际使用的会话。
	MessageID string // 最终持久化回复标识。
	Text      string // 最终回复正文。
	TakenOver bool   // 执行是否已由前台接管。
	Outcome   string // 执行终态。
}

// BackgroundCopyRequest 复制后台历史，不激活目标前台会话。
type BackgroundCopyRequest struct {
	SourceSessionID string                     // 被复制的后台会话。
	Scope           session.Scope              // 目标会话作用域。
	TaskType        string                     // 目标后台来源。
	TaskID          string                     // 来源内的任务标识。
	Name            string                     // 目标标题。
	Metadata        map[string]json.RawMessage // 目标来源元数据，保留各字段的 JSON 值类型。
}

// ErrForegroundSession 表示后台复制的来源已不再允许后台操作。
var ErrForegroundSession = session.ErrForegroundSession

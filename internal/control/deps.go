package control

import (
	"context"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/doctor"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

// SupervisorDeps 只借用能力，不取得提供方组件或生命周期。未接入的能力返回 not_configured。
type SupervisorDeps struct {
	Tools              ToolCapabilities                                     // 原始工具目录查询。
	Skills             SkillCapabilities                                    // 技能重载与删除。
	FileRollback       FileRollbackCapabilities                             // 原始备份记录与撤销。
	PrepareFileContext func(context.Context, bool) (context.Context, error) // Agent 提供绑定与提交准入，bool 表示写入。

	Sessions      SessionCapabilities                          // 会话业务裁决，包含其自身的写入准入。
	History       HistoryCapabilities                          // 会话快照和分支原始消息读取。
	Requests      RequestCapabilities                          // 请求查询与取消，不负责实际退出。
	Execution     ExecutionCapabilities                        // Agent 的执行事实、压缩与停止。
	Models        ModelCapabilities                            // 模型目录、协议检查和选择提交。
	Confirmations ConfirmationCapabilities                     // 复用原有确认等待对象，不建立第二套确认状态。
	Hooks         HookCapabilities                             // Hook 根包适配后的管理能力。
	Logs          LogCapabilities                              // 结构化日志查询。
	Doctor        DoctorCapabilities                           // 当前磁盘配置只读检查。
	Runtime       RuntimeCapabilities                          // 预留重启请求，实际关闭和重建由宿主负责。
	Commands      func(context.Context) ([]CommandInfo, error) // 提供命令元数据，C 按角色过滤。
	Scope         func(context.Context) session.Scope          // 从可信调用上下文确定会话范围，不从请求字段构造。
	Status        func(context.Context) (StatusResult, error)  // Agent 提供的一致状态快照。
	LogDir        string                                       // 运行中日志中心的实际目录；为空则不开放原始日志根。
}

type SessionCapabilities interface {
	ResetCurrent(context.Context, session.Scope) error
	Current(context.Context, session.Scope) (*storage.Session, error)
	CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
	DefaultMode() string
	ActivateMode(context.Context, session.Scope, session.ActivateModeRequest) (session.ActivateModeResult, error)
	ListPage(context.Context, session.Scope, session.ListRequest) (session.ListResult, error)
	Resume(context.Context, session.Scope, string) (*storage.Session, error)
	ResumeLast(context.Context, session.Scope) (*storage.Session, error)
	Fork(context.Context, session.Scope, string) (*storage.Session, error)
	Rename(context.Context, session.Scope, string, string) (*storage.Session, error)
	Archive(context.Context, session.Scope, string) (*storage.Session, error)
	Unarchive(context.Context, session.Scope, string) (*storage.Session, error)
	Pin(context.Context, session.Scope, string) (*storage.Session, error)
	Unpin(context.Context, session.Scope, string) (*storage.Session, error)
	Delete(context.Context, session.Scope, string) error
	CleanupExpired(context.Context, time.Time) (int, error)
	EnterBinding(context.Context, *session.Binding) (context.Context, func(), error)
	EnterSessions(context.Context, ...string) (context.Context, func(), error)
}

// HistoryCapabilities 由 Agent 根部适配自己的仓库与 Loader，不向 C 交付整个 Store。
type HistoryCapabilities interface {
	GetSession(context.Context, string) (*storage.Session, error)
	LoadMessagePage(context.Context, string, int, int) (contextmgr.MessagePage, error)
}

type RequestCapabilities interface {
	List() []request.Request
	Get(string) (request.Request, bool)
	Cancel(string) bool
	CancelSession(string) int
}

type ExecutionCapabilities interface {
	SnapshotAll() []turn.Snapshot
	CompactCurrent(context.Context, string) (*storage.Session, error)
	StopSession(string)
}

type ModelCapabilities interface {
	Resolve(config.ModelSelection) modelmgr.Selection
	ReloadProviders(context.Context) error
	PrepareModel(string) (modelmgr.ModelOption, error)
	CommitModelForMode(string, modelmgr.ModelOption) (modelmgr.ModelOption, error)
	SelectCompactModel(string) (modelmgr.ModelOption, error)
	SelectNamingModel(string) (modelmgr.ModelOption, error)
	ModelList(string, modelmgr.ModelListOptions) modelmgr.ModelListResult
}

// ConfirmationCapabilities 查询并响应当前确认等待项。
type ConfirmationCapabilities interface {
	Current(context.Context, CurrentConfirmationRequest) (CurrentConfirmationResult, error)
	Respond(context.Context, RespondConfirmationRequest) (RespondConfirmationResult, error)
}

type HookCapabilities interface {
	List(context.Context) ([]HookInfo, error)
	Reload(context.Context) (ReloadHooksResult, error)
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Restart(context.Context, string) error
}

type LogCapabilities interface {
	Query(context.Context, logging.LogQuery) (logging.LogQueryResult, error)
}
type DoctorCapabilities interface {
	Check(context.Context) (doctor.Report, error)
}

// PrepareRestart 只校验并预留一次重启，不停止接入；结果交付后由调用方执行 Commit，否则 Cancel。
// 提供方负责拒绝重复预留，以及等待旧实例真实退出后重建运行实例。
type RuntimeCapabilities interface {
	PrepareRestart(context.Context) (RestartPreparation, error)
}

type RestartPreparation struct {
	Commit func(context.Context) error // 只提交已预留的重启请求，不同步等待整个实例退出。
	Cancel func()                      // 释放尚未交付的预留；不得触发重启。
}

// ContextData 保留上游原始用量；未知值继续为 nil。
type ContextData struct {
	Usage               *llm.Usage `json:"usage"`
	ContextWindow       int        `json:"context_window"`
	CompactEnabled      bool       `json:"compact_enabled"`
	CompactTriggerRatio float64    `json:"compact_trigger_ratio"`
}

type ToolCapabilities interface {
	QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
	LoadToolDetail(context.Context, string) (tool.DetailBlock, error)
}
type SkillCapabilities interface {
	Reload(context.Context) error
	Remove(context.Context, string) error
}
type FileRollbackCapabilities interface {
	List(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, uint64) (fileops.RollbackResult, error)
}

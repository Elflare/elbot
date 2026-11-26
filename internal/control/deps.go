package control

import (
	"context"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/doctor"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

// SupervisorDeps 只借用能力，不取得提供方组件或生命周期。
type SupervisorDeps struct {
	AgentDeps                     // Agent 根部在构造阶段接入的能力。
	Models    ModelCapabilities   // 模型目录、协议检查和选择提交。
	Hooks     HookCapabilities    // Hook 根包适配后的管理能力。
	Logs      LogCapabilities     // 结构化日志查询。
	Doctor    DoctorCapabilities  // 当前磁盘配置只读检查。
	Runtime   RuntimeCapabilities // 预留重启请求，实际关闭和重建由宿主负责。
	LogDir    string              // 运行中日志中心的实际目录；为空则不开放原始日志根。
}

type SessionCapabilities interface {
	CreateUnbound(context.Context, session.Scope) (operationresult.Result[*storage.Session], error)
	ResetCurrent(context.Context, session.Scope) (operationresult.Result[struct{}], error)
	Current(context.Context, session.Scope) (operationresult.Result[*storage.Session], error)
	CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
	DefaultMode() string
	ActivateMode(context.Context, session.Scope, session.ActivateModeRequest) (operationresult.Result[session.ActivateModeResult], error)
	ListPage(context.Context, session.Scope, session.ListRequest) (operationresult.Result[session.ListResult], error)
	Resume(context.Context, session.Scope, string) (operationresult.Result[*storage.Session], error)
	ResumeLast(context.Context, session.Scope, bool) (operationresult.Result[*storage.Session], error)
	Fork(context.Context, session.Scope, string) (operationresult.Result[*storage.Session], error)
	Rename(context.Context, session.Scope, string, string) (operationresult.Result[*storage.Session], error)
	Archive(context.Context, session.Scope, string) (operationresult.Result[*storage.Session], error)
	Unarchive(context.Context, session.Scope, string) (operationresult.Result[*storage.Session], error)
	Pin(context.Context, session.Scope, string) (operationresult.Result[*storage.Session], error)
	Unpin(context.Context, session.Scope, string) (operationresult.Result[*storage.Session], error)
	Delete(context.Context, session.Scope, string) (operationresult.Result[struct{}], error)
	CleanupExpired(context.Context, time.Time) (int, error)
	EnterBinding(context.Context, *session.Binding) (context.Context, func(), error)
	EnterSessions(context.Context, ...string) (context.Context, func(), error)
}

// HistoryCapabilities 由 Agent 根部适配自己的仓库与 Loader，不向 C 交付整个 Store。
type HistoryCapabilities interface {
	GetSession(context.Context, string) (*storage.Session, error)
	ListInputHistory(context.Context, contextinfo.ActorIdentity, int) ([]string, error)
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
	CompactCurrent(context.Context, string) (operationresult.Result[*storage.Session], error)
	StopSession(string)
}

type ModelCapabilities interface {
	Resolve(config.ModelSelection) modelmgr.Selection
	ReloadProviders(context.Context) error
	PrepareModel(string) (operationresult.Result[modelmgr.ModelOption], error)
	CommitModelForMode(string, modelmgr.ModelOption) (operationresult.Result[modelmgr.ModelOption], error)
	SelectCompactModel(string) (operationresult.Result[modelmgr.ModelOption], error)
	SelectNamingModel(string) (operationresult.Result[modelmgr.ModelOption], error)
	ModelList(string, modelmgr.ModelListOptions) modelmgr.ModelListResult
}

// ConfirmationCapabilities 查询并响应当前确认等待项。
type ConfirmationCapabilities interface {
	Current(context.Context, CurrentConfirmationRequest) (operationresult.Result[CurrentConfirmationResult], error)
	Respond(context.Context, RespondConfirmationRequest) (operationresult.Result[RespondConfirmationResult], error)
}

type HookCapabilities interface {
	List(context.Context) ([]HookInfo, error)
	Reload(context.Context) (ReloadHooksResult, error)
	Start(context.Context, string) (operationresult.Result[struct{}], error)
	Stop(context.Context, string) (operationresult.Result[struct{}], error)
	Restart(context.Context, string) (operationresult.Result[struct{}], error)
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
	PrepareRestart(context.Context) (operationresult.Result[RestartPreparation], error)
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
	LoadToolDetail(context.Context, string) (operationresult.Result[tool.DetailBlock], error)
}
type SkillCapabilities interface {
	Reload(context.Context) error
	Remove(context.Context, string) (operationresult.Result[struct{}], error)
}
type FileRollbackCapabilities interface {
	List(context.Context) operationresult.Result[[]fileops.RollbackInfo]
	RollbackByID(context.Context, uint64) (operationresult.Result[fileops.RollbackResult], error)
}

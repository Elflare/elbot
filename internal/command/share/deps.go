// deps.go 定义内置命令实际消费的外部能力，不取得这些资源的生命周期。
package share

import (
	"context"
	"time"

	"elbot/internal/config"
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

type SessionService interface {
	ActivateMode(context.Context, session.Scope, session.ActivateModeRequest) (session.ActivateModeResult, error)
	Archive(context.Context, session.Scope, string) (*storage.Session, error)
	CleanupExpired(context.Context, time.Time) (int, error)
	Current(context.Context, session.Scope) (*storage.Session, error)
	CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
	DefaultMode() string
	Delete(context.Context, session.Scope, string) error
	EnterBinding(context.Context, *session.Binding) (context.Context, func(), error)
	EnterSessions(context.Context, ...string) (context.Context, func(), error)
	Fork(context.Context, session.Scope, string) (*storage.Session, error)
	List(context.Context, session.Scope, string, int) ([]storage.SessionSummary, error)
	ListPage(context.Context, session.Scope, string, int, int, bool) ([]storage.SessionSummary, error)
	ListResumablePage(context.Context, session.Scope, int, int) ([]storage.SessionSummary, error)
	Pin(context.Context, session.Scope, string) (*storage.Session, error)
	Rename(context.Context, session.Scope, string, string) (*storage.Session, error)
	ResetCurrent(context.Context, session.Scope) error
	Resume(context.Context, session.Scope, string) (*storage.Session, error)
	Status(context.Context, session.Scope) (*session.Status, error)
	Unarchive(context.Context, session.Scope, string) (*storage.Session, error)
	Unpin(context.Context, session.Scope, string) (*storage.Session, error)
}

type RequestService interface {
	Cancel(string) bool
	CancelSession(string) int
	Get(string) (request.Request, bool)
	List() []request.Request
	ListBySession(string) []request.Request
}

type TurnService interface {
	Snapshot(string) turn.Snapshot
	SnapshotAll() []turn.Snapshot
	StopSession(string)
}

type ModelService interface {
	Resolve(config.ModelSelection) modelmgr.Selection
	ReloadProviders(context.Context) error
	CurrentModelForMode(string) modelmgr.ModelOption
	CurrentCompactModel(string) modelmgr.ModelOption
	PrepareModel(string) (modelmgr.ModelOption, error)
	CommitModelForMode(string, modelmgr.ModelOption) (modelmgr.ModelOption, error)
	SelectCompactModel(string) (modelmgr.ModelOption, error)
	SelectNamingModel(string) (modelmgr.ModelOption, error)
	ModelList(string, modelmgr.ModelListOptions) modelmgr.ModelListResult
}

type ContextService interface {
	Status(context.Context, *llm.Usage, config.ModelSelection) string
}

type CompactService interface {
	CompactCurrent(context.Context, string) (string, error)
}

type ToolService interface {
	QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
}

type SkillService interface {
	Remove(context.Context, string) error
	Reload(context.Context) error
}

type LogService interface {
	Query(context.Context, logging.LogQuery) (logging.LogQueryResult, error)
}

type DoctorService interface {
	Check(context.Context) (doctor.Report, error)
}

type FileRollbackService interface {
	List(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, uint64) (fileops.RollbackResult, error)
}

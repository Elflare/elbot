// deps.go 公开内置命令装配所消费的能力，不暴露 builtin 内部状态。
package command

import (
	"context"

	"elbot/internal/command/share"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type SessionService = share.SessionService
type RequestService = share.RequestService
type TurnService = share.TurnService
type ModelService = share.ModelService
type ContextService = share.ContextService
type CompactService = share.CompactService
type ToolService = share.ToolService
type SkillService = share.SkillService
type LogService = share.LogService
type DoctorService = share.DoctorService
type FileRollbackService = share.FileRollbackService

// BuiltinDeps 借用内置命令需要的业务能力；路由和命令状态由 Command 根部装配。
type BuiltinDeps struct {
	Doctor               DoctorService                                        // 配置与内置 Skill 的只读诊断能力。
	Sessions             SessionService                                       // 会话查询、管理和操作准入。
	Requests             RequestService                                       // 请求查询及取消。
	Turns                TurnService                                          // 会话执行状态查询及停止。
	Store                storage.Store                                        // 历史加载器及会话、用量查询所需的公共存储。
	Scope                func(context.Context) session.Scope                  // 从本次调用上下文确定操作范围。
	Models               ModelService                                         // 模型目录、归属检查和选择。
	Compact              CompactService                                       // 当前会话的压缩入口。
	Contexts             ContextService                                       // 当前上下文用量展示。
	Tools                ToolService                                          // 工具目录查询。
	Skills               SkillService                                         // Skill 重载和移除。
	FileRollback         FileRollbackService                                  // 文件备份列表及撤销操作。
	PrepareFileContext   func(context.Context, bool) (context.Context, error) // 准备文件命令所需的工作目录与准入。
	Logs                 LogService                                           // 日志查询。
	Usage                func(*storage.Session) (*llm.Usage, error)           // 读取已保存的会话用量。
	SessionListPageSize  int                                                  // 会话列表每页条数。
	SessionRetentionDays int                                                  // 清理会话时使用的保留天数。
}

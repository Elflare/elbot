// 汇总内置工具定义，连接工具共享的依赖。
package builtin

import (
	"context"
	"time"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/processenv"
	"elbot/internal/storage"
	"elbot/internal/tool/builtin/chat_history"
	"elbot/internal/tool/builtin/long_memory"
	"elbot/internal/tool/builtin/weave"
	"elbot/internal/tool/builtin/web"
	"elbot/internal/tool/share"
)

type Deps struct {
	CreateQuestions     func([]storage.Question, bool) *storage.QuestionGroup // Assign IDs and save one question group.
	QuestionCommands    func() (string, string)                               // Configured answer and submit commands.
	ResidentMemory      ResidentMemoryService                                 // 当前用户的常驻记忆能力。
	ResidentMemoryPath  string                                                // 常驻记忆文件保护范围。
	LongMemoryDir       string                                                // 长期记忆资产与索引目录。
	SkillRoot           string                                                // 技能资产保护范围。
	FileRollback        *fileops.Service                                      // 与总管共享的编辑和回滚服务。
	ProcessEnv          processenv.Environment                                // 已合并配置的子进程环境。
	ElwispCreatorConfig ElwispCreatorConfigProvider                           // 创建指引所需的配置摘要。
	Cron                CronRepository                                        // 已适配的任务存储能力。
	ValidateModel       func(string, string) operationresult.Result[struct{}] // Cron 模型选择校验。
	Now                 func() time.Time                                      // Cron 时钟。
	ViewImageAvailable  func(context.Context) bool                            // 当前调用上下文的看图资格。
	Media               interface {
		ValidateImage(context.Context, string) (operationresult.Result[share.MediaInfo], error)
		ImportURL(context.Context, string, share.MediaImportOptions) (operationresult.Result[share.MediaInfo], error)
		ImportFile(context.Context, string, share.MediaImportOptions) (operationresult.Result[share.MediaInfo], error)
		Metadata(context.Context, string) (operationresult.Result[share.MediaInfo], error)
		NewCall() share.MediaCall
	} // 媒体工具实际使用的能力。
	ChatHistory chat_history.Deps
}

func NewTools(deps Deps) []share.ToolDefinition {
	guard := FileGuard{NewElSkillFileGuardRule(deps.SkillRoot), NewResidentMemoryFileGuardRule(deps.ResidentMemoryPath), NewLongMemoryFileGuardRule(deps.LongMemoryDir)}
	deps.FileRollback.CheckWrite = guard.CheckWrite
	definitions := []share.ToolDefinition{
		weave.Definition(deps.ProcessEnv),
		askUserTool{deps.CreateQuestions, deps.QuestionCommands}.definition(),
		(WorkspaceTool{}).Definition(),
		(SendFileTool{media: deps.Media}).Definition(),
		NewReadFileTool(guard).Definition(),
		(EditFileTool{Rollback: deps.FileRollback}).Definition(),
		(RollbackFileTool{Service: deps.FileRollback}).Definition(),
		(ShellTool{ProcessEnv: deps.ProcessEnv, FileGuard: guard, Media: deps.Media}).Definition(),
		(ElwispCreatorTool{config: deps.ElwispCreatorConfig}).Definition(),
		web.NewWebSearchTool(deps.ProcessEnv).Definition(),
		web.NewWebExtractTool(deps.ProcessEnv).Definition(),
	}
	deps.ChatHistory.LocalSourcePath = localSourcePath
	deps.ChatHistory.SensitivePath = isSensitiveReadFile
	deps.ChatHistory.ViewImageAvailable = deps.ViewImageAvailable
	definitions = append(definitions, chat_history.Definitions(deps.ChatHistory)...)
	definitions = append(definitions, NewResidentMemoryTools(deps.ResidentMemory)...)
	definitions = append(definitions, long_memory.NewLongMemoryTools(deps.LongMemoryDir)...)
	return append(definitions, NewCronTools(deps.Cron, deps.ValidateModel, deps.Now)...)
}

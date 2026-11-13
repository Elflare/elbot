// 汇总内置工具定义，连接工具共享的依赖。
package builtin

import (
	"context"
	"time"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/processenv"
	"elbot/internal/tool/builtin/long_memory"
	"elbot/internal/tool/builtin/web"
	"elbot/internal/tool/share"
)

type Deps struct {
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
	ChatHistory interface {
		Search(context.Context, share.SearchRequest) (operationresult.Result[[]share.Message], error)
		Around(context.Context, share.AroundRequest) (operationresult.Result[[]share.Message], error)
		GetMessage(context.Context, string) (operationresult.Result[share.Message], error)
		GetImages(context.Context, share.MediaRequest) (operationresult.Result[[]share.MediaResult], error)
		GetMedia(context.Context, share.MediaRequest) (operationresult.Result[[]share.MediaResult], error)
	} // 聊天历史工具实际使用的能力。
}

func NewTools(deps Deps) []share.ToolDefinition {
	guard := FileGuard{NewElSkillFileGuardRule(deps.SkillRoot), NewResidentMemoryFileGuardRule(deps.ResidentMemoryPath), NewLongMemoryFileGuardRule(deps.LongMemoryDir)}
	deps.FileRollback.CheckWrite = guard.CheckWrite
	definitions := []share.ToolDefinition{
		(WorkspaceTool{}).Definition(),
		(ViewImageTool{center: deps.Media, history: deps.ChatHistory}).Definition(),
		(SendFileTool{media: deps.Media}).Definition(),
		(GetMediaTool{history: deps.ChatHistory}).Definition(),
		(SearchChatHistoryTool{history: deps.ChatHistory, viewImageAvailable: deps.ViewImageAvailable}).Definition(),
		(GetChatHistoryAroundTool{history: deps.ChatHistory, viewImageAvailable: deps.ViewImageAvailable}).Definition(),
		(ReplyToChatHistoryMessageTool{history: deps.ChatHistory}).Definition(),
		NewReadFileTool(guard).Definition(),
		(EditFileTool{Rollback: deps.FileRollback}).Definition(),
		(RollbackFileTool{Service: deps.FileRollback}).Definition(),
		(ShellTool{ProcessEnv: deps.ProcessEnv, FileGuard: guard, Media: deps.Media}).Definition(),
		(ElwispCreatorTool{config: deps.ElwispCreatorConfig}).Definition(),
		web.NewWebSearchTool(deps.ProcessEnv).Definition(),
		web.NewWebExtractTool(deps.ProcessEnv).Definition(),
	}
	definitions = append(definitions, NewResidentMemoryTools(deps.ResidentMemory)...)
	definitions = append(definitions, long_memory.NewLongMemoryTools(deps.LongMemoryDir)...)
	return append(definitions, NewCronTools(deps.Cron, deps.ValidateModel, deps.Now)...)
}

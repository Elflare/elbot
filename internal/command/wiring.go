package command

import "elbot/internal/command/builtin"

// RegisterBuiltins 在给定路由中登记内置命令，并持有这组命令共用的会话展示状态。
func RegisterBuiltins(router *Router, deps BuiltinDeps) error {
	return builtin.RegisterDefaultModules(router, builtin.Deps{
		CommandText:        router.commandText,
		Router:             router,
		Doctor:             deps.Doctor,
		Sessions:           deps.Sessions,
		Requests:           deps.Requests,
		Turns:              deps.Turns,
		Store:              deps.Store,
		Scope:              deps.Scope,
		Models:             deps.Models,
		Compact:            deps.Compact,
		Contexts:           deps.Contexts,
		Tools:              deps.Tools,
		Skills:             deps.Skills,
		FileRollback:       deps.FileRollback,
		PrepareFileContext: deps.PrepareFileContext,
		Logs:               deps.Logs,
		Usage:              deps.Usage,
		SessionState:       builtin.NewSessionCommandState(deps.SessionListPageSize, deps.SessionRetentionDays),
	})
}

package command

import "elbot/internal/command/builtin"

// RegisterBuiltins 在给定路由中登记内置命令并注入配置。
func RegisterBuiltins(router *Router, deps BuiltinDeps) error {
	return builtin.RegisterDefaultModules(router, builtin.Deps{
		CommandText:          router.commandText,
		Router:               router,
		Doctor:               deps.Doctor,
		Sessions:             deps.Sessions,
		Requests:             deps.Requests,
		Turns:                deps.Turns,
		Store:                deps.Store,
		Scope:                deps.Scope,
		Models:               deps.Models,
		Compact:              deps.Compact,
		Contexts:             deps.Contexts,
		Tools:                deps.Tools,
		Skills:               deps.Skills,
		FileRollback:         deps.FileRollback,
		PrepareFileContext:   deps.PrepareFileContext,
		Logs:                 deps.Logs,
		Usage:                deps.Usage,
		SessionListPageSize:  deps.SessionListPageSize,
		SessionRetentionDays: deps.SessionRetentionDays,
	})
}

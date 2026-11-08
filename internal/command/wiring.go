package command

import "elbot/internal/command/builtin"

func RegisterBuiltins(router *Router, deps BuiltinDeps) error {
	return builtin.RegisterDefaultModules(router, builtin.Deps{Control: deps.Control, CommandText: router.commandText, SessionListPageSize: deps.SessionListPageSize, SessionRetentionDays: deps.SessionRetentionDays})
}

package share

import "elbot/internal/contextinfo"

type ConfirmationCommand struct {
	Info
	Scope, Decision string
}

// ConfirmationCommands is shared by registration, completion and the risk prompt.
func ConfirmationCommands() []ConfirmationCommand {
	return []ConfirmationCommand{
		{Info: Info{Name: "detail", Aliases: []string{"details"}, Description: "详情", MinRole: contextinfo.RoleUser}},
		{Info: Info{Name: "confirm", Aliases: []string{"c"}, Args: "[附加输入]", Description: "确认本次", MinRole: contextinfo.RoleUser}, Scope: "current", Decision: "approve"},
		{Info: Info{Name: "confirmtool", Aliases: []string{"ct"}, Args: "[附加输入]", Description: "确认工具", MinRole: contextinfo.RoleUser}, Scope: "tool", Decision: "approve"},
		{Info: Info{Name: "confirmall", Aliases: []string{"ca"}, Args: "[附加输入]", Description: "确认全部", MinRole: contextinfo.RoleUser}, Scope: "all", Decision: "approve"},
		{Info: Info{Name: "reject", Args: "[原因]", Description: "拒绝", MinRole: contextinfo.RoleUser}, Scope: "current", Decision: "reject"},
	}
}

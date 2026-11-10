package confirmation

import (
	"elbot/internal/delivery"
)

type riskConfirmationCommand struct {
	Name        string
	Aliases     []string
	Description string
}

func riskConfirmationCommands() []riskConfirmationCommand {
	return []riskConfirmationCommand{
		{Name: "detail", Aliases: []string{"details"}, Description: "详情"},
		{Name: "confirm", Aliases: []string{"c"}, Description: "确认本次"},
		{Name: "confirmtool", Aliases: []string{"ct"}, Description: "确认工具"},
		{Name: "confirmall", Aliases: []string{"ca"}, Description: "确认全部"},
		{Name: "reject", Description: "拒绝"},
		{Name: "stop", Description: "停止"},
	}
}

func RiskConfirmationCommandNames() []string {
	commands := riskConfirmationCommands()
	names := make([]string, 0, len(commands)*2)
	for _, cmd := range commands {
		names = append(names, cmd.Name)
		names = append(names, cmd.Aliases...)
	}
	return names
}

func riskConfirmationActions(commandText func(string, ...string) string) *delivery.Confirmation {
	presentation := &delivery.Confirmation{Actions: []delivery.Action{}}
	for _, item := range riskConfirmationCommands() {
		presentation.Actions = append(presentation.Actions, delivery.Action{ID: item.Name, Label: item.Description, Command: commandText(item.Name)})
	}
	return presentation
}

func AppendConfirmationActions(commandText func(string, ...string) string) []delivery.Action {
	return []delivery.Action{
		{ID: "append_confirm", Label: "继续发送", Command: commandText("confirm")},
		{ID: "append_cancel", Label: "取消追加", Command: commandText("stop")},
	}
}

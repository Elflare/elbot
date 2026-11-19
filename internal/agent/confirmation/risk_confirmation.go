package confirmation

import (
	command "elbot/internal/command/share"
	"elbot/internal/delivery"
)

func riskConfirmationCommands() []command.Info {
	var infos []command.Info
	for _, spec := range command.ConfirmationCommands() {
		infos = append(infos, spec.Info)
	}
	return append(infos, command.Info{Name: "stop", Description: "停止"})
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

func riskConfirmationActions(commandText func(string, ...string) string) []delivery.Action {
	var actions []delivery.Action
	for _, item := range riskConfirmationCommands() {
		name := item.Name
		for _, alias := range item.Aliases {
			if len(alias) < len(name) {
				name = alias
			}
		}
		actions = append(actions, delivery.Action{ID: item.Name, Label: item.Description, Command: commandText(name)})
	}
	return actions
}

func AppendConfirmationActions(commandText func(string, ...string) string) []delivery.Action {
	return []delivery.Action{
		{ID: "append_confirm", Label: "继续发送", Command: commandText("confirm")},
		{ID: "append_cancel", Label: "取消追加", Command: commandText("stop")},
	}
}

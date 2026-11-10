package confirmation

import (
	"fmt"
	"strings"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
)

type riskConfirmationCommand struct {
	Name        string
	Aliases     []string
	Args        string
	Description string
}

func riskConfirmationCommands() []riskConfirmationCommand {
	return []riskConfirmationCommand{
		{Name: "detail", Aliases: []string{"details"}, Args: "", Description: "详情"},
		{Name: "confirm", Aliases: []string{"c"}, Args: "", Description: "确认本次"},
		{Name: "confirmtool", Aliases: []string{"ct"}, Args: "", Description: "确认工具"},
		{Name: "confirmall", Aliases: []string{"ca"}, Args: "", Description: "确认全部"},
		{Name: "reject", Args: "[原因]", Description: "拒绝"},
		{Name: "stop", Args: "", Description: "停止"},
	}
}

func riskConfirmationCommandHelp(commandText func(string, ...string) string) string {
	parts := make([]string, 0, len(riskConfirmationCommands()))
	for i, cmd := range riskConfirmationCommands() {
		name := cmd.Name
		for _, alias := range cmd.Aliases {
			if len(alias) < len(name) {
				name = alias
			}
		}
		text := commandText(name, cmd.Args) + ": " + cmd.Description
		if i%2 == 0 {
			parts = append(parts, text)
		} else {
			parts[len(parts)-1] += " | " + text
		}
	}
	return strings.Join(parts, "\n")
}

func riskConfirmationPromptText(timeout time.Duration, commandText func(string, ...string) string) string {
	text := riskConfirmationCommandHelp(commandText)
	if timeout > 0 {
		text += fmt.Sprintf("\nidle=%s → stop · %s: 重置计时", agentshare.ConfirmationWaitDurationText(timeout), commandText("detail"))
	}
	return text
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

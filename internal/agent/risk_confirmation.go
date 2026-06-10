package agent

import (
	"fmt"
	"strings"

	"elbot/internal/command"
	"elbot/internal/turn"
)

type riskConfirmationCommand struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
}

func riskConfirmationCommands() []riskConfirmationCommand {
	return []riskConfirmationCommand{
		{Name: "detail", Aliases: []string{"details"}, Usage: "/detail", Description: "查看完整参数"},
		{Name: "confirm", Aliases: []string{"c"}, Usage: "/confirm", Description: "确认本次"},
		{Name: "confirmtool", Aliases: []string{"ct"}, Usage: "/confirmtool", Description: "确认本次并自动确认后续同工具调用"},
		{Name: "confirmall", Aliases: []string{"ca"}, Usage: "/confirmall", Description: "确认本次并自动确认当前 Session 后续所有工具调用"},
		{Name: "reject", Usage: "/reject <原因>", Description: "拒绝"},
		{Name: "stop", Usage: "/stop", Description: "停止当前请求"},
	}
}

func riskConfirmationCommandHelp() string {
	parts := make([]string, 0, len(riskConfirmationCommands()))
	for _, cmd := range riskConfirmationCommands() {
		parts = append(parts, cmd.Usage+" "+cmd.Description)
	}
	return strings.Join(parts, "；")
}

func riskConfirmationPromptText() string {
	return "可用指令：" + riskConfirmationCommandHelp()
}

func riskConfirmationWaitingText() string {
	return "正在等待高风险工具调用确认，只接受：" + riskConfirmationCommandHelp() + "。\n"
}

func riskConfirmationDetailText(confirmation turn.RiskConfirmation) string {
	args := strings.TrimSpace(confirmation.Arguments)
	if args == "" {
		args = "{}"
	}
	return fmt.Sprintf("高风险工具调用详情\n工具：%s\n风险：%s\n参数：\n%s\n", confirmation.ToolName, confirmation.Risk, args)
}

func riskConfirmationCommandNames() []string {
	commands := riskConfirmationCommands()
	names := make([]string, 0, len(commands)*2)
	for _, cmd := range commands {
		names = append(names, cmd.Name)
		names = append(names, cmd.Aliases...)
	}
	return names
}

func isRiskConfirmationCommandName(name string) bool {
	name = strings.TrimSpace(name)
	for _, candidate := range riskConfirmationCommandNames() {
		if name == candidate {
			return true
		}
	}
	return false
}

func isRiskConfirmationCommand(text string, router *command.Router) bool {
	if router == nil {
		return false
	}
	parsed := router.Parse(text)
	return parsed.OK && isRiskConfirmationCommandName(parsed.Name)
}

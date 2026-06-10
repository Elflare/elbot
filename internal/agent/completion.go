package agent

import (
	"context"
	"strings"

	"elbot/internal/contextmgr"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// Complete returns command completions for platform adapters that support them.
// TODO: 后续改为更模块化的补全服务，由 app 层装配后注入 CLI，而不是通过 Agent 暴露。
// TODO: 后续扩展更多参数感知补全。
func (a *Agent) Complete(text string) []string {
	if out := a.completeRiskConfirmationCommand(text); len(out) > 0 {
		return out
	}
	if out := a.completeForkMessageID(text); len(out) > 0 {
		return out
	}
	return a.commands.Complete(text)
}

func (a *Agent) completeRiskConfirmationCommand(text string) []string {
	session, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
	if err != nil || a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		return nil
	}
	parsed := a.commands.Parse(text)
	if !parsed.OK || hasAnyCommandArgs(text, parsed.Prefix) {
		return nil
	}
	commands := riskConfirmationCommandNames()
	out := []string{}
	for _, name := range commands {
		if strings.HasPrefix(name, parsed.Name) {
			out = append(out, parsed.Prefix+name)
		}
	}
	return out
}

func (a *Agent) completeForkMessageID(text string) []string {
	parsed := a.commands.Parse(text)
	if !parsed.OK || parsed.Name != "fork" || !hasForkArgs(text, parsed.Prefix) {
		return nil
	}
	session, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
	if err != nil {
		return nil
	}
	loaded, err := (contextmgr.Loader{Store: a.store}).Load(context.Background(), session.ID)
	if err != nil {
		return nil
	}
	prefix := strings.TrimSpace(parsed.Args)
	out := []string{}
	for _, message := range loaded.Messages {
		if message.Role != storage.RoleAssistant || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if strings.HasPrefix(message.ID, prefix) {
			out = append(out, parsed.Prefix+"fork "+message.ID)
		}
	}
	return out
}

func hasAnyCommandArgs(text, prefix string) bool {
	text = strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(text, prefix) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	_, args, ok := strings.Cut(rest, " ")
	return ok && strings.TrimSpace(args) != ""
}

func hasForkArgs(text, prefix string) bool {
	text = strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(text, prefix) {
		return false
	}
	rest := strings.TrimPrefix(text, prefix)
	_, args, ok := strings.Cut(rest, " ")
	return ok && strings.HasPrefix(strings.TrimLeft(rest, " \t"), "fork") && (args == "" || strings.TrimSpace(args) != "")
}

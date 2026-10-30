// commands.go 接收命令目录，转换并同步平台命令。
package telegram

import (
	"context"
	"elbot/internal/command"
	"strings"
	"unicode"
)

func (a *Adapter) SetCommandCatalog(infos []command.Info) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commandCatalog = append([]command.Info(nil), infos...)
}

func (a *Adapter) syncBotCommands(ctx context.Context) error {
	a.mu.Lock()
	infos := append([]command.Info(nil), a.commandCatalog...)
	a.mu.Unlock()
	commands := telegramBotCommands(infos)
	if len(commands) == 0 {
		return nil
	}
	return a.client.setMyCommands(ctx, commands)
}

func telegramBotCommands(infos []command.Info) []botCommand {
	out := make([]botCommand, 0, len(infos))
	seen := map[string]bool{}
	for _, info := range infos {
		name := strings.TrimSpace(strings.TrimPrefix(info.Name, "/"))
		if !validTelegramCommandName(name) || seen[name] {
			continue
		}
		description := strings.TrimSpace(info.Description)
		if description == "" {
			description = strings.TrimSpace(info.Name + " " + info.Args)
		}
		if description == "" {
			continue
		}
		out = append(out, botCommand{Command: name, Description: truncateRunes(description, 256)})
		seen[name] = true
	}
	return out
}

func validTelegramCommandName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if r == '_' || unicode.IsDigit(r) || ('a' <= r && r <= 'z') {
			continue
		}
		return false
	}
	return true
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

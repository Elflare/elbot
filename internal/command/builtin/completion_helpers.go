package builtin

import (
	"context"
	"elbot/internal/tool"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/request"
)

func completeStringOptions(values []string, query string, start, end int, kind string) []command.Completion {
	out := []command.Completion{}
	for _, value := range values {
		if value == "" || !strings.HasPrefix(value, query) {
			continue
		}
		out = append(out, command.Completion{Text: value, Label: value, Kind: kind, ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

func completeSessionIDs(ctx context.Context, deps Deps, query string, archived bool, start, end int) []command.Completion {
	if deps.Sessions == nil || deps.Scope == nil {
		return nil
	}
	sessions, _, err := listSessionPage(ctx, deps, "", 1, sessionPageSize(deps), archived)
	if err != nil {
		return nil
	}
	out := []command.Completion{}
	for _, session := range sessions {
		if !strings.HasPrefix(session.ID, query) {
			continue
		}
		out = append(out, command.Completion{Text: session.ID, Label: session.ID, Description: emptyTitle(session.Title), Kind: "session_id", ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

func completeRequestIDs(requests []request.Request, query string, start, end int) []command.Completion {
	out := []command.Completion{}
	for _, req := range requests {
		if !strings.HasPrefix(req.ID, query) {
			continue
		}
		out = append(out, command.Completion{Text: req.ID, Label: req.ID, Description: strings.TrimSpace(string(req.Kind) + " " + req.Label), Kind: "request_id", ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

func completeToolNames(ctx context.Context, deps Deps, query string, start, end int) []command.Completion {
	if deps.Tools == nil {
		return nil
	}
	infos, err := deps.Tools.QueryTools(ctx, tool.ToolQuery{Scope: tool.QueryCatalog, IncludeHidden: true, IncludeDiscover: true})
	if err != nil {
		return nil
	}
	out := []command.Completion{}
	for _, info := range infos {
		if !strings.HasPrefix(info.Definition.Name, query) {
			continue
		}
		out = append(out, command.Completion{Text: info.Definition.Name, Label: info.Definition.Name, Description: info.Definition.Description, Kind: "tool_name", ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

func completeConfirmFlag(args string, token command.CompletionToken) []command.Completion {
	if strings.Contains(" "+args+" ", " --confirm ") {
		return nil
	}
	return command.CompleteStaticOptions([]command.CompletionOption{{Text: "--confirm", Description: "确认操作"}}, token.Text, token.Start, token.End, "option")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

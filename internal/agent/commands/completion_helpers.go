package commands

import (
	"context"
	"strings"

	"elbot/internal/command"
)

type completionToken struct {
	Start int
	End   int
	Text  string
}

func currentCompletionToken(req command.CompletionRequest) completionToken {
	cursor := req.Cursor
	if cursor <= 0 || cursor > len(req.Raw) {
		cursor = len(req.Raw)
	}
	start := cursor
	for start > 0 && req.Raw[start-1] != ' ' && req.Raw[start-1] != '\t' {
		start--
	}
	return completionToken{Start: start, End: cursor, Text: req.Raw[start:cursor]}
}

func completeStaticOptions(options []completionOption, query string, start, end int, kind string) []command.Completion {
	out := []command.Completion{}
	for _, option := range options {
		if query != "" && !strings.HasPrefix(option.Text, query) {
			continue
		}
		out = append(out, command.Completion{Text: option.Text, Label: firstNonEmpty(option.Label, option.Text), Description: option.Description, Kind: kind, ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

type completionOption struct {
	Text        string
	Label       string
	Description string
}

func completeStringOptions(values []string, query string, start, end int, kind string) []command.Completion {
	out := []command.Completion{}
	for _, value := range values {
		if value == "" || (query != "" && !strings.HasPrefix(value, query)) {
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
		if query != "" && !strings.HasPrefix(session.ID, query) {
			continue
		}
		out = append(out, command.Completion{Text: session.ID, Label: session.ID, Description: emptyTitle(session.Title), Kind: "session_id", ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

func completeRequestIDs(deps Deps, query string, start, end int) []command.Completion {
	if deps.Requests == nil {
		return nil
	}
	out := []command.Completion{}
	for _, req := range deps.Requests.List() {
		if query != "" && !strings.HasPrefix(req.ID, query) {
			continue
		}
		out = append(out, command.Completion{Text: req.ID, Label: req.ID, Description: strings.TrimSpace(string(req.Kind) + " " + req.Label), Kind: "request_id", ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

func completeToolNames(deps Deps, query string, start, end int) []command.Completion {
	if deps.Tools == nil {
		return nil
	}
	out := []command.Completion{}
	for _, info := range deps.Tools.List() {
		if query != "" && !strings.HasPrefix(info.Name, query) {
			continue
		}
		out = append(out, command.Completion{Text: info.Name, Label: info.Name, Description: info.Description, Kind: "tool_name", ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

func completeConfirmFlag(args string, token completionToken) []command.Completion {
	if strings.Contains(" "+args+" ", " --confirm ") {
		return nil
	}
	return completeStaticOptions([]completionOption{{Text: "--confirm", Description: "Confirm this destructive action"}}, token.Text, token.Start, token.End, "option")
}

func isFirstArg(req command.CompletionRequest, token completionToken) bool {
	before := strings.TrimSpace(req.Raw[len(req.Prefix)+len(req.Name) : token.Start])
	return before == ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

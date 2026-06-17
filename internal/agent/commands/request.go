package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"elbot/internal/command"
	"elbot/internal/request"
	"elbot/internal/turn"
)

type RequestInfo struct {
	Request      request.Request
	SessionTitle string
	SessionMode  string
	Tools        map[string]int
}

func NewRequests(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "requests",
		Usage:       "/requests",
		Description: "List active requests.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		requests := deps.Requests.List()
		if len(requests) == 0 {
			return &command.Result{Content: "no active requests"}, nil
		}
		return &command.Result{Content: formatRequests(ctx, deps, requests)}, nil
	})
}

func NewStop(deps Deps) command.Handler {
	return stopCommand{deps: deps}
}

type stopCommand struct {
	deps Deps
}

func (c stopCommand) Info() command.Info {
	return command.Info{
		Name:        "stop",
		Usage:       "/stop [request_id]",
		Description: "Stop a request or all requests in current session.",
	}
}

func (c stopCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	arg := strings.TrimSpace(req.Args)
	if arg != "" {
		if !deps.Requests.Cancel(arg) {
			return &command.Result{Content: fmt.Sprintf("request not found: %s", arg)}, nil
		}
		for _, snapshot := range deps.Turns.SnapshotAll() {
			deps.Turns.StopSession(snapshot.SessionID)
		}
		return &command.Result{Content: "stopped 1 request"}, nil
	}

	current, err := deps.Sessions.Current(ctx, deps.Scope(ctx))
	if err != nil {
		return nil, err
	}
	count := deps.Requests.CancelSession(current.ID)
	deps.Turns.StopSession(current.ID)
	return &command.Result{Content: fmt.Sprintf("stopped %d request%s", count, plural(count))}, nil
}

func (c stopCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	token := currentCompletionToken(req)
	if !isFirstArg(req, token) {
		return nil
	}
	return completeRequestIDs(c.deps, token.Text, token.Start, token.End)
}

func NewStopAll(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "stopall",
		Usage:       "/stopall",
		Description: "Stop all active requests in this process.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		count := deps.Requests.CancelAll()
		deps.Turns.StopAll()
		return &command.Result{Content: fmt.Sprintf("stopped %d request%s", count, plural(count))}, nil
	})
}

func formatRequests(ctx context.Context, deps Deps, requests []request.Request) string {
	var sb strings.Builder
	sb.WriteString("active requests:\n")
	for _, req := range requests {
		snapshot := deps.Turns.Snapshot(req.SessionID)
		session, _ := deps.Store.Sessions().Get(ctx, req.SessionID)
		title := "(unknown)"
		mode := "(unknown)"
		if session != nil {
			if session.Title != "" {
				title = session.Title
			}
			mode = session.Mode
		}
		sb.WriteString(fmt.Sprintf("  %s %s %s %s\n", req.ID, req.Kind, req.Label, formatDuration(time.Since(req.StartedAt))))
		sb.WriteString(fmt.Sprintf("    session: %s\n", title))
		sb.WriteString(fmt.Sprintf("    mode: %s\n", mode))
		if tools := turn.ToolsString(snapshot.Tools); tools != "" {
			sb.WriteString(fmt.Sprintf("    tools: %s", tools))
		}
		sb.WriteString("\n")
	}
	return trimTrailingNewlines(sb.String())
}

func formatActiveRequests(requests []request.Request) string {
	if len(requests) == 0 {
		return "active requests: none"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("active requests: %d\n", len(requests)))
	for _, req := range requests {
		sb.WriteString(fmt.Sprintf("  %s %s %s %s\n", req.ID, req.Kind, req.Label, formatDuration(time.Since(req.StartedAt))))
	}
	return trimTrailingNewlines(sb.String())
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	return d.Round(time.Second).String()
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

type RequestModule struct{}

func (RequestModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewRequests,
		NewStop,
		NewStopAll,
	)
}

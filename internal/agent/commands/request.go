package commands

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"elbot/internal/command"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
	"elbot/internal/turn"
)

type RequestInfo struct {
	Request      request.Request
	SessionTitle string
	SessionMode  string
	Tools        map[string]int
}

type requestTree struct {
	Roots    []requestNode
	ByNumber map[string]request.Request
}

type requestNode struct {
	Request  request.Request
	Children []request.Request
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
		Usage:       "/stop [request_id|number]",
		Description: "Stop a request or all requests in current session. Use /requests to see numbers like 1 or 1.1.",
		MinRole:     security.RoleUser,
	}
}

func (c stopCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	arg := strings.TrimSpace(req.Args)
	if arg != "" {
		id, ok := resolveRequestArg(deps, arg)
		if !ok {
			return &command.Result{Content: fmt.Sprintf("request not found: %s", arg)}, nil
		}
		stopped, _ := deps.Requests.Get(id)
		if stopped.Kind == request.KindTurn {
			count := deps.Requests.CancelSession(stopped.SessionID)
			deps.Turns.StopSession(stopped.SessionID)
			return &command.Result{Content: fmt.Sprintf("stopped %d request%s", count, plural(count))}, nil
		}
		if !deps.Requests.Cancel(id) {
			return &command.Result{Content: fmt.Sprintf("request not found: %s", arg)}, nil
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
	return formatRequestTree(ctx, deps, requests, false)
}

func formatRequestTree(ctx context.Context, deps Deps, requests []request.Request, showNone bool) string {
	if len(requests) == 0 && showNone {
		return "active requests: none"
	}
	tree := buildRequestTree(requests)
	var sb strings.Builder
	sb.WriteString("active requests:")
	if len(requests) > 0 {
		sb.WriteString("\n")
	}
	for i, root := range tree.Roots {
		number := strconv.Itoa(i + 1)
		writeRequestLine(&sb, ctx, deps, number, "", root.Request)
		for j, child := range root.Children {
			childNumber := fmt.Sprintf("%s.%d", number, j+1)
			writeRequestLine(&sb, ctx, deps, childNumber, "└── ", child)
		}
	}
	return trimTrailingNewlines(sb.String())
}

func writeRequestLine(sb *strings.Builder, ctx context.Context, deps Deps, number, prefix string, req request.Request) {
	kind := string(req.Kind)
	label := strings.TrimSpace(req.Label)
	if req.Kind == request.KindTurn {
		if label == "" {
			label = "chat"
		}
		kind = "turn"
	} else if label == "" {
		label = "request"
	}
	sb.WriteString(fmt.Sprintf("  %s[%s] %s %s request %s\n", prefix, number, label, kind, formatDuration(time.Since(req.StartedAt))))
	if prefix == "" {
		session, _ := deps.Store.Sessions().Get(ctx, req.SessionID)
		title := "(unknown)"
		mode := "(unknown)"
		if session != nil {
			if session.Title != "" {
				title = session.Title
			}
			mode = session.Mode
		}
		sb.WriteString(fmt.Sprintf("      session: %s\n", title))
		if deps.RuntimeStatus != nil {
			snapshot := deps.RuntimeStatus(req.SessionID)
			if snapshot.Phase != "" && snapshot.Phase != runtimestatus.PhaseIdle && snapshot.Phase != runtimestatus.PhaseDone {
				phaseText := string(snapshot.Phase)
				if snapshot.Phase == runtimestatus.PhaseTool && snapshot.ToolName != "" {
					phaseText += " " + snapshot.ToolName
				}
				sb.WriteString(fmt.Sprintf("      phase: %s (%s)\n", phaseText, formatDuration(snapshot.StageElapsed(time.Now()))))
			}
		}
		sb.WriteString(fmt.Sprintf("      mode: %s\n", mode))
		if tools := turn.ToolsString(deps.Turns.Snapshot(req.SessionID).Tools); tools != "" {
			sb.WriteString(fmt.Sprintf("      tools: %s\n", tools))
		}
		return
	}
	if req.Kind == request.KindTool {
		sb.WriteString(fmt.Sprintf("      tool: %s\n", label))
	}
	if req.Kind == request.KindHook {
		sb.WriteString(fmt.Sprintf("      hook: %s\n", label))
	}
}

func resolveRequestArg(deps Deps, arg string) (string, bool) {
	if deps.Requests == nil {
		return "", false
	}
	if req, ok := deps.Requests.Get(arg); ok {
		return req.ID, true
	}
	tree := buildRequestTree(deps.Requests.List())
	req, ok := tree.ByNumber[arg]
	if !ok {
		return "", false
	}
	return req.ID, true
}

func buildRequestTree(requests []request.Request) requestTree {
	byID := map[string]request.Request{}
	children := map[string][]request.Request{}
	for _, req := range requests {
		byID[req.ID] = req
		if req.ParentID != "" {
			children[req.ParentID] = append(children[req.ParentID], req)
		}
	}
	roots := []requestNode{}
	for _, req := range requests {
		if req.ParentID != "" {
			if _, ok := byID[req.ParentID]; ok {
				continue
			}
		}
		roots = append(roots, requestNode{Request: req})
	}
	sort.Slice(roots, func(i, j int) bool { return requestLess(roots[i].Request, roots[j].Request) })
	byNumber := map[string]request.Request{}
	for i := range roots {
		root := &roots[i]
		root.Children = append([]request.Request(nil), children[root.Request.ID]...)
		sort.Slice(root.Children, func(i, j int) bool { return requestLess(root.Children[i], root.Children[j]) })
		number := strconv.Itoa(i + 1)
		byNumber[number] = root.Request
		for j, child := range root.Children {
			byNumber[fmt.Sprintf("%s.%d", number, j+1)] = child
		}
	}
	return requestTree{Roots: roots, ByNumber: byNumber}
}

func requestLess(left, right request.Request) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.Before(right.StartedAt)
	}
	return left.ID < right.ID
}

func formatActiveRequests(ctx context.Context, deps Deps, requests []request.Request) string {
	return formatRequestTree(ctx, deps, requests, true)
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

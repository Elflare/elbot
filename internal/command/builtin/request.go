package builtin

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type RequestInfo struct {
	Request request.Request        // 原始请求及其执行身份。
	Session *storage.Session       // 请求所属的原始会话记录；未查到时为 nil。
	Runtime runtimestatus.Snapshot // 请求所属会话的运行状态快照。
	Turn    turn.Snapshot          // 请求所属会话的单轮状态快照，包含工具统计。
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
		MinRole:     contextinfo.RoleUser,
	}
}

func (c stopCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	arg := strings.TrimSpace(req.Args)
	requests, binding := stoppableRequests(ctx, deps)
	if arg != "" {
		id, ok := resolveRequestArg(requests, arg)
		if !ok {
			return &command.Result{Content: fmt.Sprintf("request not found: %s", arg)}, nil
		}
		stopped, _ := deps.Requests.Get(id)
		var locked context.Context
		var release func()
		var err error
		if binding != nil {
			locked, release, err = deps.Sessions.EnterBinding(ctx, binding)
		} else {
			locked, release, err = deps.Sessions.EnterSessions(ctx, stopped.SessionID)
		}
		if err != nil {
			return nil, err
		}
		defer release()
		ctx = locked
		latest, exists := deps.Requests.Get(id)
		if !exists || latest.SessionID != stopped.SessionID || (binding != nil && latest.SessionID != binding.SessionID()) {
			return &command.Result{Content: "request already stopped"}, nil
		}
		if stopped.Kind == request.KindTurn {
			deps.Turns.StopSession(stopped.SessionID)
			count := deps.Requests.CancelSession(stopped.SessionID)
			return &command.Result{Content: fmt.Sprintf("正在停止 %d 个请求。", count)}, nil
		}
		if latest.Stopping {
			return &command.Result{Content: "请求正在停止，等待执行收尾。"}, nil
		}
		if !deps.Requests.Cancel(id) {
			return &command.Result{Content: fmt.Sprintf("request not found: %s", arg)}, nil
		}
		return &command.Result{Content: "正在停止 1 个请求。"}, nil
	}

	actor, _ := contextinfo.ActorFromContext(ctx)
	if binding == nil && actor.Role == contextinfo.RoleSuperadmin {
		_, binding, _ = deps.Sessions.CurrentBound(ctx, deps.Scope(ctx))
	}
	if binding == nil {
		return &command.Result{Content: session.ErrNoCurrentSession.Error()}, nil
	}
	locked, release, err := deps.Sessions.EnterBinding(ctx, binding)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = locked
	current, err := deps.Sessions.Current(ctx, deps.Scope(ctx))
	if err != nil {
		return nil, err
	}
	deps.Turns.StopSession(current.ID)
	count := deps.Requests.CancelSession(current.ID)
	return &command.Result{Content: fmt.Sprintf("正在停止 %d 个请求。", count)}, nil
}

func (c stopCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	if !command.IsFirstArg(req, token) {
		return nil
	}
	requests, _ := stoppableRequests(ctx, c.deps)
	return completeRequestIDs(requests, token.Text, token.Start, token.End)
}

func NewStopAll(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "stopall",
		Usage:       "/stopall",
		Description: "Stop all active requests in this process.",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		ids := []string{}
		seen := map[string]bool{}
		for _, active := range deps.Turns.SnapshotAll() {
			seen[active.SessionID] = true
		}
		requests := deps.Requests.List()
		for _, active := range requests {
			seen[active.SessionID] = true
		}
		for id := range seen {
			ids = append(ids, id)
		}
		_, release, err := deps.Sessions.EnterSessions(ctx, ids...)
		if err != nil {
			return nil, err
		}
		defer release()
		count := 0
		for _, id := range ids {
			deps.Turns.StopSession(id)
			count += deps.Requests.CancelSession(id)
		}
		return &command.Result{Content: fmt.Sprintf("正在停止 %d 个请求。", count)}, nil
	})
}

func formatRequests(ctx context.Context, deps Deps, requests []request.Request) string {
	return formatRequestTree(ctx, deps, requests, false, func(sb *strings.Builder, info RequestInfo) {
		title := "(unknown)"
		if info.Session != nil && info.Session.Title != "" {
			title = info.Session.Title
		}
		sb.WriteString(fmt.Sprintf("      session: %s\n", title))
		writeRequestDetails(sb, info)
	})
}

func formatRequestTree(ctx context.Context, deps Deps, requests []request.Request, showNone bool, writeDetails func(*strings.Builder, RequestInfo)) string {
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
		writeRequestLine(&sb, number, "", root.Request)
		writeDetails(&sb, collectRequestInfo(ctx, deps, root.Request))
		for j, child := range root.Children {
			childNumber := fmt.Sprintf("%s.%d", number, j+1)
			writeRequestLine(&sb, childNumber, "└── ", child)
		}
	}
	return trimTrailingNewlines(sb.String())
}

func writeRequestLine(sb *strings.Builder, number, prefix string, req request.Request) {
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
	if req.Stopping {
		label += " stopping"
	}
	sb.WriteString(fmt.Sprintf("  %s[%s] %s %s request %s\n", prefix, number, label, kind, formatDuration(time.Since(req.StartedAt))))
	if prefix == "" {
		return
	}
	if req.Kind == request.KindTool {
		sb.WriteString(fmt.Sprintf("      tool: %s\n", label))
	}
	if req.Kind == request.KindHook {
		sb.WriteString(fmt.Sprintf("      hook: %s\n", label))
	}
}

func collectRequestInfo(ctx context.Context, deps Deps, req request.Request) RequestInfo {
	info := RequestInfo{Request: req}
	info.Session, _ = deps.Store.Sessions().Get(ctx, req.SessionID)
	if deps.RuntimeStatus != nil {
		info.Runtime = deps.RuntimeStatus(req.SessionID)
	}
	info.Turn = deps.Turns.Snapshot(req.SessionID)
	return info
}

func writeRequestDetails(sb *strings.Builder, info RequestInfo) {
	snapshot := info.Runtime
	if snapshot.Phase != "" && snapshot.Phase != runtimestatus.PhaseIdle && snapshot.Phase != runtimestatus.PhaseDone {
		phaseText := string(snapshot.Phase)
		if snapshot.Phase == runtimestatus.PhaseTool && snapshot.ToolName != "" {
			phaseText += " " + snapshot.ToolName
		}
		sb.WriteString(fmt.Sprintf("      phase: %s (%s)\n", phaseText, formatDuration(snapshot.StageElapsed(time.Now()))))
	}
	mode := "(unknown)"
	if info.Session != nil {
		mode = info.Session.Mode
	}
	sb.WriteString(fmt.Sprintf("      mode: %s\n", mode))
	if tools := toolsString(info.Turn.Tools); tools != "" {
		sb.WriteString(fmt.Sprintf("      tools: %s\n", tools))
	}
}

// Both argument resolution and completion use the caller's cancellation scope.
func stoppableRequests(ctx context.Context, deps Deps) ([]request.Request, *session.Binding) {
	if deps.Requests == nil {
		return nil, nil
	}
	if actor, _ := contextinfo.ActorFromContext(ctx); actor.Role == contextinfo.RoleSuperadmin {
		return deps.Requests.List(), nil
	}
	_, binding, err := deps.Sessions.CurrentBound(ctx, deps.Scope(ctx))
	if err != nil {
		return nil, nil
	}
	if original, ok := session.BindingFromContext(ctx); ok && (original != binding || !original.Valid()) {
		return nil, nil
	}
	return deps.Requests.ListBySession(binding.SessionID()), binding
}

func resolveRequestArg(requests []request.Request, arg string) (string, bool) {
	for _, req := range requests {
		if req.ID == arg {
			return req.ID, true
		}
	}
	tree := buildRequestTree(requests)
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
	return formatRequestTree(ctx, deps, requests, true, writeRequestDetails)
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

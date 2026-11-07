package builtin

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/request"
	"elbot/internal/session"
)

type requestTree struct {
	Roots    []requestNode
	ByNumber map[string]string
}

type requestNode struct {
	Request  request.Request
	Children []request.Request
}

func NewRequests(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "requests",
		Args:        "",
		Description: "运行中的请求",
		Help:        "编号用于 " + deps.CommandText("stop") + "；子编号形如 1.1",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		requests := deps.Requests.List()
		if len(requests) == 0 {
			return &command.Result{Kind: command.ResultKindText, Text: "nil"}, nil
		}
		return &command.Result{Kind: command.ResultKindText, Text: formatRequests(ctx, deps, requests)}, nil
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
		Args:        "[request_id|number]",
		Description: "停止请求",
		MinRole:     contextinfo.RoleUser,
		Help: "省略=当前会话全部请求；停止主对话请求→停止该会话全部请求\n" +
			"普通用户仅限当前会话，编号见 " + c.deps.CommandText("status") + "；超管编号见 " + c.deps.CommandText("requests"),
	}
}

func (c stopCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	arg := strings.TrimSpace(req.Args)
	requests, binding := stoppableRequests(ctx, deps)
	if arg != "" {
		id, ok := resolveRequestArg(requests, arg)
		if !ok {
			return &command.Result{Kind: command.ResultKindText, Text: fmt.Sprintf("request not found: %s", arg)}, nil
		}
		stopped, _ := deps.Requests.Get(id)
		var release func()
		var err error
		if binding != nil {
			_, release, err = deps.Sessions.EnterBinding(ctx, binding)
		} else {
			_, release, err = deps.Sessions.EnterSessions(ctx, stopped.SessionID)
		}
		if err != nil {
			return nil, err
		}
		defer release()
		latest, exists := deps.Requests.Get(id)
		if !exists || latest.SessionID != stopped.SessionID || (binding != nil && latest.SessionID != binding.SessionID()) {
			return &command.Result{Kind: command.ResultKindText, Text: "stopped"}, nil
		}
		if stopped.Kind == request.KindTurn {
			deps.Turns.StopSession(stopped.SessionID)
			count := deps.Requests.CancelSession(stopped.SessionID)
			return &command.Result{Kind: command.ResultKindText, Text: fmt.Sprintf("stopping: %d", count)}, nil
		}
		if latest.Stopping {
			return &command.Result{Kind: command.ResultKindText, Text: "stopping"}, nil
		}
		if !deps.Requests.Cancel(id) {
			return &command.Result{Kind: command.ResultKindText, Text: fmt.Sprintf("request not found: %s", arg)}, nil
		}
		return &command.Result{Kind: command.ResultKindText, Text: "stopping: 1"}, nil
	}

	actor, _ := contextinfo.ActorFromContext(ctx)
	if binding == nil && actor.Role == contextinfo.RoleSuperadmin {
		_, binding, _ = deps.Sessions.CurrentBound(ctx, deps.Scope(ctx))
	}
	if binding == nil {
		return &command.Result{Kind: command.ResultKindText, Text: session.ErrNoCurrentSession.Error()}, nil
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
	return &command.Result{Kind: command.ResultKindText, Text: fmt.Sprintf("stopping: %d", count)}, nil
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
		Args:        "",
		Description: "停止所有会话的请求",
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
		return &command.Result{Kind: command.ResultKindText, Text: fmt.Sprintf("stopping: %d", count)}, nil
	})
}

func formatRequests(ctx context.Context, deps Deps, requests []request.Request) string {
	return formatRequestTree(requests, func(sb *strings.Builder, req request.Request) {
		title := "unk"
		if row, err := deps.Store.Sessions().Get(ctx, req.SessionID); err == nil && row.Title != "" {
			title = row.Title
		}
		sb.WriteString(fmt.Sprintf("      session: %s\n", title))
	})
}

func formatRequestTree(requests []request.Request, writeDetails func(*strings.Builder, request.Request)) string {
	if len(requests) == 0 {
		return ""
	}
	tree := buildRequestTree(requests)
	var sb strings.Builder
	sb.WriteString("requests:")
	sb.WriteString("\n")
	for i, root := range tree.Roots {
		number := strconv.Itoa(i + 1)
		writeRequestLine(&sb, number, "", root.Request)
		if writeDetails != nil {
			writeDetails(&sb, root.Request)
		}
		for j, child := range root.Children {
			childNumber := fmt.Sprintf("%s.%d", number, j+1)
			writeRequestLine(&sb, childNumber, "└── ", child)
		}
	}
	return trimTrailingNewlines(sb.String())
}

func writeRequestLine(sb *strings.Builder, number, prefix string, req request.Request) {
	label := strings.TrimSpace(req.Label)
	if req.Kind == request.KindTurn {
		if label == "" {
			label = "chat"
		}
	} else if label == "" {
		label = "request"
	}
	if req.Stopping {
		label += " stopping"
	}
	sb.WriteString(fmt.Sprintf("  %s[%s] %s: %s · %s\n", prefix, number, req.Kind, label, formatDuration(time.Since(req.StartedAt))))
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
	id, ok := tree.ByNumber[arg]
	return id, ok
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
	byNumber := map[string]string{}
	for i := range roots {
		root := &roots[i]
		root.Children = children[root.Request.ID]
		sort.Slice(root.Children, func(i, j int) bool { return requestLess(root.Children[i], root.Children[j]) })
		number := strconv.Itoa(i + 1)
		byNumber[number] = root.Request.ID
		for j, child := range root.Children {
			byNumber[fmt.Sprintf("%s.%d", number, j+1)] = child.ID
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

func formatActiveRequests(requests []request.Request) string {
	return formatRequestTree(requests, nil)
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

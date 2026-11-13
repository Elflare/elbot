package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type requestTree struct {
	Roots    []requestNode
	ByNumber map[string]string
}
type requestNode struct {
	Request  control.RequestData
	Children []control.RequestData
}

func NewRequests(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "requests", Description: "运行中的请求", Help: "编号用于 " + deps.CommandText("stop") + "；子编号形如 1.1"}, func(ctx context.Context, req command.Request) (command.Result, error) {
		result, err := deps.Control.ListRequests(ctx)
		if err != nil {
			return command.Result{}, err
		}
		return command.FromOperationResult(result, command.ResultKindRequests), nil
	})
}
func NewStop(deps Deps) command.Handler { return stopCommand{deps: deps} }

type stopCommand struct{ deps Deps }

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

func (c stopCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	arg := strings.TrimSpace(req.Args)
	if arg == "" {
		result, err := c.deps.Control.StopSession(ctx)
		if err != nil {
			return command.Result{}, err
		}
		return command.FromOperationResult(result, command.ResultKindStop), nil
	}
	items, err := stoppableRequests(ctx, c.deps)
	if err != nil {
		return command.Result{}, err
	}
	if items.Status != operationresult.Success {
		return command.Result{Status: items.Status, Message: items.Message}, nil
	}
	id, ok := resolveRequestArg(items.Data, arg)
	if !ok {
		return command.Result{Status: operationresult.NotFound, Message: fmt.Sprintf("request not found: %s", arg)}, nil
	}
	result, err := c.deps.Control.StopRequest(ctx, control.StopRequestRequest{RequestID: id})
	if err != nil {
		return command.Result{}, err
	}
	return command.FromOperationResult(result, command.ResultKindStop), nil
}
func (c stopCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	if !command.IsFirstArg(req, token) {
		return nil
	}
	items, err := stoppableRequests(ctx, c.deps)
	if err != nil || items.Status != operationresult.Success {
		return nil
	}
	return completeRequestIDs(items.Data, token.Text, token.Start, token.End)
}
func NewStopAll(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "stopall", Description: "停止所有会话的请求"}, func(ctx context.Context, req command.Request) (command.Result, error) {
		result, err := deps.Control.StopAllRequests(ctx)
		if err != nil {
			return command.Result{}, err
		}
		return command.FromOperationResult(result, command.ResultKindStop), nil
	})
}
func stoppableRequests(ctx context.Context, deps Deps) (operationresult.Result[[]control.RequestData], error) {
	actor, _ := contextinfo.ActorFromContext(ctx)
	if actor.Role == contextinfo.RoleSuperadmin {
		result, err := deps.Control.ListRequests(ctx)
		if err != nil {
			return operationresult.Result[[]control.RequestData]{}, err
		}
		if result.Status != operationresult.Success {
			return operationresult.Result[[]control.RequestData]{Status: result.Status, Message: result.Message}, nil
		}
		return operationresult.Result[[]control.RequestData]{Status: operationresult.Success, Data: result.Data.Items}, nil
	}
	data, err := deps.Control.Status(ctx)
	if err != nil {
		return operationresult.Result[[]control.RequestData]{}, err
	}
	if data.Status != operationresult.Success {
		return operationresult.Result[[]control.RequestData]{Status: data.Status, Message: data.Message}, nil
	}
	return operationresult.Result[[]control.RequestData]{Status: operationresult.Success, Data: data.Data.Requests}, nil
}
func resolveRequestArg(requests []control.RequestData, arg string) (string, bool) {
	for _, req := range requests {
		if req.ID == arg {
			return req.ID, true
		}
	}
	tree := buildRequestTree(requests)
	id, ok := tree.ByNumber[arg]
	return id, ok
}

func buildRequestTree(requests []control.RequestData) requestTree {
	byID := map[string]control.RequestData{}
	children := map[string][]control.RequestData{}
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

func requestLess(left, right control.RequestData) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.Before(right.StartedAt)
	}
	return left.ID < right.ID
}

type RequestModule struct{}

func (RequestModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewRequests, NewStop, NewStopAll)
}

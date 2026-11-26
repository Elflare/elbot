package builtin

import (
	"context"
	"elbot/internal/command/builtin/format"
	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"strconv"
)

func listSessionPage(ctx context.Context, deps Deps, query string, page int, archivedOnly, excludeCurrent, allPlatforms bool) (operationresult.Result[control.ListSessionsResult], error) {
	if page == 0 {
		page = 1
	}
	return deps.Control.ListSessions(ctx, control.ListSessionsRequest{Query: query, Page: page, PageSize: deps.SessionListPageSize, Archived: archivedOnly, ExcludeCurrent: excludeCurrent, AllPlatforms: allPlatforms})
}
func sessionChange(name string, result operationresult.Result[control.SessionChangeResult]) command.Result {
	commandResult := command.FromOperationResult(result, command.ResultKindSession, func(d control.SessionChangeResult) string {
		return format.SessionChange(name, d, format.MessagesOptions{PreviewLimit: 80})
	})
	if commandResult.Status == operationresult.Success {
		commandResult.Continuation = result.Data.Continuation
	}
	return commandResult
}

// The mutation has already committed. A preview failure must not turn its receipt into a failure.
func sessionPreview(ctx context.Context, deps Deps, name string, result operationresult.Result[control.SessionChangeResult]) command.Result {
	if result.Status != operationresult.Success {
		return sessionChange(name, result)
	}
	preview, err := deps.Control.SessionMessages(ctx, control.SessionMessagesRequest{SessionID: result.Data.SessionID, Page: 1, PageSize: resumeHistoryTurns * 2})
	if err == nil && preview.Status == operationresult.Success {
		result.Data.Messages = preview.Data.Items
		return sessionChange(name, result)
	}
	r := sessionChange(name, result)
	reason := preview.Message
	if err != nil {
		reason = err.Error()
	}
	r.Blocks = append(r.Blocks, delivery.ContentBlock{Kind: delivery.ContentText, Content: "会话已切换，预览读取失败：" + reason})
	return r
}
func sessionsResult(result operationresult.Result[control.ListSessionsResult], pageCommand func(string, ...string) string, name, query string, all bool) command.Result {
	if result.Status != operationresult.Success {
		return command.TextResult(result.Status, result.Message)
	}
	d := result.Data
	r := command.Result{Status: result.Status, Kind: command.ResultKindSessions, Blocks: []delivery.ContentBlock{{Kind: delivery.ContentSessionList, Content: d}}}
	add := func(id, label string, n int) {
		args := []string{}
		if name == "resume" {
			args = append(args, "--page")
		}
		args = append(args, strconv.Itoa(n))
		if query != "" {
			args = append(args, "-k", query)
		}
		if all {
			args = append(args, "--all")
		}
		r.Actions = append(r.Actions, delivery.Action{ID: id, Label: label, Command: pageCommand(name, args...)})
	}
	if d.Page.Page > 1 {
		add("prev", "上一页", d.Page.Page-1)
	}
	if d.HasNext {
		add("next", "下一页", d.Page.Page+1)
	}
	return r
}

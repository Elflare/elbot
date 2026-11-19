package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/control/format"
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

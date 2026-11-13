package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/operationresult"
)

func listSessionPage(ctx context.Context, deps Deps, query string, page int, archivedOnly, excludeCurrent, allPlatforms bool) (operationresult.Result[control.ListSessionsResult], error) {
	if page == 0 {
		page = 1
	}
	return deps.Control.ListSessions(ctx, control.ListSessionsRequest{Query: query, Page: page, PageSize: deps.SessionListPageSize, Archived: archivedOnly, ExcludeCurrent: excludeCurrent, AllPlatforms: allPlatforms})
}
func sessionChange(result operationresult.Result[control.SessionChangeResult]) command.Result {
	commandResult := command.FromOperationResult(result, command.ResultKindSession)
	if commandResult.Status == operationresult.Success {
		commandResult.Continuation = result.Data.Continuation
	}
	return commandResult
}
func sessionsResult(result operationresult.Result[control.ListSessionsResult]) command.Result {
	return command.FromOperationResult(result, command.ResultKindSessions)
}

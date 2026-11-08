package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/control"
)

func listSessionPage(ctx context.Context, deps Deps, query string, page int, archivedOnly, excludeCurrent bool) (control.ListSessionsResult, error) {
	if page == 0 {
		page = 1
	}
	return deps.Control.ListSessions(ctx, control.ListSessionsRequest{Query: query, Page: page, PageSize: deps.SessionListPageSize, Archived: archivedOnly, ExcludeCurrent: excludeCurrent})
}
func sessionChange(data control.SessionChangeResult) *command.Result {
	return &command.Result{Kind: command.ResultKindSession, Data: data, Continuation: data.Continuation}
}
func sessionsResult(data control.ListSessionsResult) *command.Result {
	return &command.Result{Kind: command.ResultKindSessions, Data: data}
}

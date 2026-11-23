// Package coagent 管理独立 Session 的协作关系和共享留言板。
package coagent

import (
	"context"

	"strings"

	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
	"elbot/internal/workspace"
)

type Service struct {
	Boards     storage.CoagentRepository
	AllocateID func(context.Context, share.IDOrigin) (string, error)
}

func (s *Service) Create(ctx context.Context, main *storage.Session, goal string, names []string, user bool) (operationresult.Result[storage.CoagentBoard], error) {
	goal = strings.TrimSpace(goal)
	if main.Mode != storage.SessionModeWork || main.TaskType == "coagent" {
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Forbidden, Message: "请在主工作会话中创建成员"}, nil
	}
	rows := make([]*storage.Session, 0, len(names))
	idOrigin, err := share.SessionIDOrigin(main)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	fields := storage.SessionMetadata{}
	if err := idOrigin.Save(fields); err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	origin, _, err := share.Origin(main)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	if err := share.InheritOrigin(origin, fields); err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	if err := fields.Set("title_renamed", true); err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	meta, err := fields.Encode()
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	dir := workspace.DecodeState(main.Metadata).Dir
	meta = workspace.UpdateState(meta, func(state *workspace.State) { state.SetAgentDir(dir) })
	for _, name := range names {
		id, err := s.AllocateID(ctx, idOrigin)
		if err != nil {
			return operationresult.Result[storage.CoagentBoard]{}, err
		}
		rows = append(rows, &storage.Session{ID: id, Owner: main.Owner, Platform: main.Platform, ChatType: main.ChatType, ChatID: main.ChatID, Mode: storage.SessionModeWork, TaskType: "coagent", Title: name, Metadata: meta})
	}
	author := main.ID
	if user {
		author = ""
	}
	board, err := s.Boards.AddMembers(ctx, main.ID, goal, author, rows)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Success, Data: board}, nil
}

// Package coagent 管理独立 Session 的协作关系和共享留言板。
package coagent

import (
	"context"
	"errors"
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

func (s *Service) Create(ctx context.Context, main *storage.Session, goal string, names []string, user bool) (operationresult.Result[storage.CoagentEntry], error) {
	goal = strings.TrimSpace(goal)
	if main.Mode != storage.SessionModeWork || main.TaskType == "coagent" {
		return operationresult.Result[storage.CoagentEntry]{Status: operationresult.Forbidden, Message: "请在主工作会话中创建成员"}, nil
	}
	if goal == "" || len(names) == 0 {
		return operationresult.Result[storage.CoagentEntry]{Status: operationresult.InvalidArguments, Message: "需要任务目标及成员名字"}, nil
	}
	rows := make([]*storage.Session, 0, len(names))
	idOrigin, err := share.SessionIDOrigin(main)
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	fields := storage.SessionMetadata{}
	if err := idOrigin.Save(fields); err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	origin, _, err := share.Origin(main)
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	if err := share.InheritOrigin(origin, fields); err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	if err := fields.Set("title_renamed", true); err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	meta, err := fields.Encode()
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	dir := workspace.DecodeState(main.Metadata).Dir
	meta = workspace.UpdateState(meta, func(state *workspace.State) { state.SetAgentDir(dir) })
	seen := map[string]bool{"主会话": true}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || strings.ContainsAny(name, "\r\n") {
			return operationresult.Result[storage.CoagentEntry]{Status: operationresult.InvalidArguments, Message: "成员名应为组内唯一的非空单行文本"}, nil
		}
		seen[name] = true
		id, err := s.AllocateID(ctx, idOrigin)
		if err != nil {
			return operationresult.Result[storage.CoagentEntry]{}, err
		}
		rows = append(rows, &storage.Session{ID: id, Owner: main.Owner, Platform: main.Platform, ChatType: main.ChatType, ChatID: main.ChatID, Mode: storage.SessionModeWork, TaskType: "coagent", Title: name, Metadata: meta})
	}
	author := main.ID
	if user {
		author = ""
	}
	entry, err := s.Boards.AddMembers(ctx, main.ID, goal, author, rows)
	if errors.Is(err, storage.ErrCoagentLimit) {
		return operationresult.Result[storage.CoagentEntry]{Status: operationresult.Conflict, Message: err.Error()}, nil
	}
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	return operationresult.Result[storage.CoagentEntry]{Status: operationresult.Success, Data: entry}, nil
}

package control

import (
	"context"
	"math"

	"elbot/internal/control/share"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// CoagentStatus 只暴露当前协作组的成员身份和运行状态，不携带私有历史或请求内容。
type CoagentStatus struct {
	SessionID    string     `json:"session_id"`
	Name         string     `json:"name"`
	Phase        turn.Phase `json:"phase"`
	PendingCount int        `json:"pending_count"`
}

func (m *Supervisor) CoagentStatus(ctx context.Context, target string) (operationresult.Result[[]CoagentStatus], error) {
	board, err := m.Coagent(ctx, session.CoagentAction{Operation: "board", After: math.MaxInt64}, true)
	if err != nil || board.Status != operationresult.Success {
		return operationresult.Result[[]CoagentStatus]{Status: board.Status, Message: board.Message}, err
	}
	snapshots := m.deps.Execution.SnapshotAll()
	var items []CoagentStatus
	for _, member := range board.Data.Members {
		if target != "" && member.SessionID != target {
			continue
		}
		item := CoagentStatus{SessionID: member.SessionID, Name: member.Name, Phase: turn.PhaseIdle}
		for _, snapshot := range snapshots {
			if snapshot.SessionID == member.SessionID {
				item.Phase, item.PendingCount = snapshot.Phase, snapshot.PendingCount
				break
			}
		}
		items = append(items, item)
	}
	if target != "" && len(items) == 0 {
		return operationresult.Result[[]CoagentStatus]{Status: operationresult.NotFound, Message: "该 Session 不在当前协作组"}, nil
	}
	return operationresult.Result[[]CoagentStatus]{Status: operationresult.Success, Data: items}, nil
}
func (m *Supervisor) Coagent(ctx context.Context, action session.CoagentAction, user bool) (operationresult.Result[storage.CoagentBoard], error) {
	auth, err := share.Authorize(ctx, false)
	if err != nil || auth.Status != operationresult.Success {
		return operationresult.Result[storage.CoagentBoard]{Status: auth.Status, Message: auth.Message}, err
	}
	return m.deps.Coagents(ctx, action, user)
}

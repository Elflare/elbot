package control

import (
	"context"
	"elbot/internal/control/share"
	globalevents "elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"encoding/json"
	"log/slog"
)

func (m *Supervisor) ListRollbacks(ctx context.Context) (operationresult.Result[[]fileops.RollbackInfo], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[[]fileops.RollbackInfo]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[[]fileops.RollbackInfo]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	prepared, err := m.deps.PrepareFileContext(ctx, false)
	if err != nil {
		return operationresult.Result[[]fileops.RollbackInfo]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[[]fileops.RollbackInfo]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	return m.deps.FileRollback.List(prepared.Data), nil
}
func (m *Supervisor) RollbackByID(ctx context.Context, req RollbackByIDRequest) (operationresult.Result[fileops.RollbackResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[fileops.RollbackResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[fileops.RollbackResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if req.ID == 0 {
		return operationresult.Result[fileops.RollbackResult]{Status: operationresult.InvalidArguments, Message: "id 必须为正数"}, nil
	}
	prepared, err := m.deps.PrepareFileContext(ctx, true)
	if err != nil {
		return operationresult.Result[fileops.RollbackResult]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[fileops.RollbackResult]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	ctx = prepared.Data
	rolledBack, err := m.deps.FileRollback.RollbackByID(ctx, req.ID)
	if err != nil {
		return operationresult.Result[fileops.RollbackResult]{}, err
	}
	if rolledBack.Status != operationresult.Success {
		return operationresult.Result[fileops.RollbackResult]{Status: rolledBack.Status, Message: rolledBack.Message}, nil
	}
	result := rolledBack.Data
	binding, _ := session.BindingFromContext(ctx)
	if result.Path != "" {
		sessionID := ""
		if binding != nil {
			sessionID = binding.SessionID()
		}
		content := map[string]any{"actor": authorized.Data, "path": result.Path, "deleted": result.Deleted}
		record := globalevents.LogRecord{Category: globalevents.LogCategoryAudit, Module: "control", Level: slog.LevelInfo, EventName: "file_rollback", Type: globalevents.ContentJSON, ResultStatus: globalevents.ResultSucceeded}
		record.Identity = globalevents.LogIdentityFromContext(ctx)
		record.Identity.SessionID = sessionID
		data, _ := json.Marshal(content)
		record.Content = string(data)
		globalevents.Log(ctx, record)
	}

	return operationresult.Result[fileops.RollbackResult]{Status: operationresult.Success, Data: result}, nil
}

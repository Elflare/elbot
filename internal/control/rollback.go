package control

import (
	"context"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/session"
	"elbot/internal/storage"
	"encoding/json"
	"errors"
	"log/slog"
)

func (m *Supervisor) ListRollbacks(ctx context.Context) ([]fileops.RollbackInfo, error) {
	if err := authorize(ctx, true, m.deps.FileRollback != nil && m.deps.PrepareFileContext != nil); err != nil {
		return nil, err
	}
	prepared, err := m.deps.PrepareFileContext(ctx, false)
	if errors.Is(err, storage.ErrNotFound) {
		return []fileops.RollbackInfo{}, nil
	}
	if err != nil {
		return nil, ClassifyError(err)
	}
	items, err := m.deps.FileRollback.List(prepared)
	return items, ClassifyError(err)
}
func (m *Supervisor) RollbackByID(ctx context.Context, req RollbackByIDRequest) (fileops.RollbackResult, error) {
	if err := authorize(ctx, true, m.deps.FileRollback != nil && m.deps.PrepareFileContext != nil); err != nil {
		return fileops.RollbackResult{}, err
	}
	if req.ID == 0 {
		return fileops.RollbackResult{}, invalid("id 必须为正数")
	}
	prepared, err := m.deps.PrepareFileContext(ctx, true)
	if err != nil {
		return fileops.RollbackResult{}, ClassifyError(err)
	}
	ctx = prepared
	result, err := m.deps.FileRollback.RollbackByID(ctx, req.ID)
	actor, _ := contextinfo.ActorFromContext(ctx)
	binding, _ := session.BindingFromContext(ctx)
	if result.Path != "" {
		sessionID := ""
		if binding != nil {
			sessionID = binding.SessionID()
		}
		content := map[string]any{"actor": actor, "path": result.Path, "deleted": result.Deleted}
		record := globalevents.LogRecord{Category: globalevents.LogCategoryAudit, Module: "control", Level: slog.LevelInfo, EventName: "file_rollback", Type: globalevents.ContentJSON, ResultStatus: globalevents.ResultSucceeded}
		if err != nil {
			content["error"] = err.Error()
			record.EventName = "file_rollback_failed"
			record.Level = slog.LevelError
			record.ResultStatus = globalevents.ResultFailed
			if errors.Is(err, context.Canceled) {
				record.Level = slog.LevelInfo
				record.ResultStatus = globalevents.ResultCanceled
			}
		}
		record.Identity = globalevents.LogIdentityFromContext(ctx)
		record.Identity.SessionID = sessionID
		data, _ := json.Marshal(content)
		record.Content = string(data)
		globalevents.Log(ctx, record)
	}

	return result, ClassifyError(err)
}

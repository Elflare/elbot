// 职责：按指定 assistant 消息及完整工具批次边界创建前台分支。
// 锁外准备材料，提交前复核来源和绑定，成功后激活新会话。
package branch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

// Fork 从业务准入外调用，材料准备期间不持有 Scope 或 Session 租约。
func (s *Service) Fork(ctx context.Context, scope share.Scope, fromMessageID string) (operationresult.Result[*storage.Session], error) {
	fromMessageID = strings.TrimSpace(fromMessageID)
	if fromMessageID == "" {
		return operationresult.Result[*storage.Session]{Status: operationresult.InvalidArguments, Message: "message id is required"}, nil
	}
	message, err := s.deps.Messages.Get(ctx, fromMessageID)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "分支来源消息不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	sourceID := message.SessionID
	var source *storage.Session
	var original *share.Binding
	var origin llm.Origin
	var boundary string
	initial, err := func() (operationresult.Result[struct{}], error) {
		locked, release, err := s.deps.Bindings.EnterActivation(ctx, scope, sourceID)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		defer release()
		original = s.deps.Bindings.CurrentBinding(scope)
		if binding, ok := share.BindingFromContext(ctx); ok && binding != original {
			return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "session binding expired"}, nil
		}
		if original != nil && s.sessionBusy(original.SessionID()) {
			return operationresult.Result[struct{}]{Status: operationresult.Busy, Message: "会话正忙"}, nil
		}
		source, err = s.deps.Sessions.Get(locked, sourceID)
		if errors.Is(err, storage.ErrNotFound) {
			return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: "分支来源会话不存在"}, nil
		}
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if !canForkAccess(scope, source) {
			return operationresult.Result[struct{}]{Status: operationresult.Forbidden, Message: "session is not accessible in current scope"}, nil
		}
		origin, _, err = share.Origin(source)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		message, err = s.deps.Messages.Get(locked, fromMessageID)
		if errors.Is(err, storage.ErrNotFound) {
			return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: "分支来源消息不存在"}, nil
		}
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		checked, err := s.forkBoundary(locked, sourceID, message)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: checked.Status, Message: checked.Message}, nil
		}
		boundary = checked.Data
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}()
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if initial.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: initial.Status, Message: initial.Message}, nil
	}
	prepared, err := s.prepareMaterial(ctx, origin, source, message)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	idOrigin := share.ForegroundIDOrigin(scope)
	nextID, err := s.deps.AllocateID(ctx, idOrigin)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	locked, release, err := s.deps.Bindings.EnterActivation(ctx, scope, sourceID, nextID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	defer release()
	if s.deps.Bindings.CurrentBinding(scope) != original {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "session binding expired"}, nil
	}
	if original != nil && s.sessionBusy(original.SessionID()) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Busy, Message: "会话正忙"}, nil
	}
	latest, err := s.deps.Sessions.Get(locked, sourceID)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "分支来源会话不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if !canForkAccess(scope, latest) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Forbidden, Message: "session is not accessible in current scope"}, nil
	}
	after, _, err := share.Origin(latest)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if origin != after {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "source session origin changed"}, nil
	}
	message, err = s.deps.Messages.Get(locked, fromMessageID)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "分支来源消息不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	latestBoundary, err := s.forkBoundary(locked, sourceID, message)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if latestBoundary.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: latestBoundary.Status, Message: latestBoundary.Message}, nil
	}
	if boundary != latestBoundary.Data {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "source fork boundary changed"}, nil
	}
	fields := storage.SessionMetadata{}
	if err := share.InheritOrigin(after, fields); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if err := idOrigin.Save(fields); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	metadata, err := fields.Encode()
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	mode := latest.Mode
	if share.IsBackground(latest) {
		mode = storage.SessionModeWork
	}
	row := &storage.Session{ID: nextID, ParentSessionID: sourceID, ForkFromMessageID: boundary,
		Owner: scope.Owner, Platform: scope.Platform, ChatType: scope.ChatType, ChatID: scope.ChatID,
		Mode: mode, Status: storage.SessionStatusActive, Title: forkTitle(latest.Title), Metadata: metadata}
	if prepared.Data == nil {
		err = s.deps.Sessions.Create(locked, row)
	} else {
		err = s.deps.Sessions.CreateMaterial(locked, materialCreate(row, sourceID, prepared.Data))
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	s.deps.Bindings.SetCurrent(locked, scope, row.ID)
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: row}, nil
}

func canForkAccess(scope share.Scope, row *storage.Session) bool {
	return scope.IsSuperadmin || share.CanAccess(scope, row)
}

func (s *Service) forkBoundary(ctx context.Context, sourceID string, message *storage.Message) (operationresult.Result[string], error) {
	if message.SessionID != sourceID || message.Role != storage.RoleAssistant {
		return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: "can only fork from a source assistant message"}, nil
	}
	fields, err := storage.DecodeSessionMetadata(message.Metadata)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if message.ToolBatchStatus == "pending" {
		return operationresult.Result[string]{Status: operationresult.Conflict, Message: "cannot fork an unfinished tool batch"}, nil
	}
	var resultIDs []string
	if raw, ok := fields[storage.ToolResultMessageKey]; ok {
		if err := json.Unmarshal(raw, &resultIDs); err != nil {
			return operationresult.Result[string]{}, err
		}
	}
	if len(resultIDs) == 0 {
		return operationresult.Result[string]{Status: operationresult.Success, Data: message.ID}, nil
	}
	result, err := s.deps.Messages.Get(ctx, resultIDs[len(resultIDs)-1])
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if result.SessionID != sourceID || result.Role != storage.RoleTool {
		return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: "invalid tool batch fork boundary"}, nil
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: result.ID}, nil
}

func forkTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New session"
	}
	title = "Fork: " + title
	if utf8.RuneCountInString(title) > 40 {
		return string([]rune(title)[:40]) + "..."
	}
	return title
}

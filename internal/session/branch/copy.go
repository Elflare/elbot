// 职责：创建后台会话副本并复制历史与材料。
// 重映射消息和工具结果关联，不继承前台绑定或运行中的执行状态。
package branch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

type BackgroundCopyRequest struct {
	SourceSessionID string                  // 要复制的未接管后台会话。
	TaskType        string                  // 副本后台来源种类。
	TaskID          string                  // 副本所属任务名称。
	Name            string                  // 副本标题；空值按种类和名称生成。
	Metadata        storage.SessionMetadata // 调用方的业务字段，不由本方法原地修改。
}

// CopyForeground creates a foreground copy when a superadmin resumes a session from another platform.
func (s *Service) CopyForeground(ctx context.Context, target share.Scope, sourceID string) (operationresult.Result[*storage.Session], bool, error) {
	if !target.IsSuperadmin {
		return operationresult.Result[*storage.Session]{Status: operationresult.Forbidden, Message: "无权访问该会话"}, false, nil
	}
	source, err := s.deps.Sessions.Get(ctx, sourceID)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "来源会话不存在"}, false, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	if source.Platform == target.Platform {
		return operationresult.Result[*storage.Session]{Status: operationresult.Success}, false, nil
	}
	origin, _, err := share.Origin(source)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	prepared, err := s.prepareMaterial(ctx, origin, source, nil)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: prepared.Status, Message: prepared.Message}, false, nil
	}
	idOrigin := share.ForegroundIDOrigin(target)
	nextID, err := s.deps.AllocateID(ctx, idOrigin)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	locked, release, err := s.deps.Bindings.EnterActivation(ctx, target, sourceID, nextID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	defer release()
	current := s.deps.Bindings.CurrentBinding(target)
	if original, ok := share.BindingFromContext(ctx); ok && original != current {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "session binding expired"}, false, nil
	}
	if current != nil && s.sessionBusy(current.SessionID()) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Busy, Message: "会话正忙"}, false, nil
	}
	if s.sessionBusy(sourceID) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Busy, Message: "会话正忙"}, false, nil
	}
	latest, err := s.deps.Sessions.Get(locked, sourceID)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "来源会话不存在"}, false, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	if latest.Platform == target.Platform {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "source session platform changed"}, false, nil
	}
	latestOrigin, _, err := share.Origin(latest)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	if origin != latestOrigin {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "source session origin changed"}, false, nil
	}
	messages, err := s.deps.Messages.ListBySession(locked, sourceID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	toolCalls, err := s.deps.ToolCalls.ListBySession(locked, sourceID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	fields, err := storage.DecodeSessionMetadata(latest.Metadata)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	delete(fields, "llm_checkpoint")
	delete(fields, "llm_seed")
	if err := idOrigin.Save(fields); err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	mode := latest.Mode
	if share.IsBackground(latest) {
		mode = storage.SessionModeWork
		if err := fields.Set("foreground_origin", map[string]any{
			"task_type": latest.TaskType, "task_id": latest.TaskID, "name": latest.TaskName,
			"owner": latest.Owner, "platform": latest.Platform,
		}); err != nil {
			return operationresult.Result[*storage.Session]{}, false, err
		}
	}
	metadata, err := fields.Encode()
	if err != nil {
		return operationresult.Result[*storage.Session]{}, false, err
	}
	row := &storage.Session{ID: nextID, ParentSessionID: sourceID, Owner: target.Owner, Platform: target.Platform,
		ChatType: target.ChatType, ChatID: target.ChatID, Mode: mode, Status: storage.SessionStatusActive,
		Title: latest.Title, Metadata: metadata}
	request := materialCreate(row, sourceID, prepared.Data)
	copied, err := s.copySession(locked, request, messages, toolCalls)
	if err != nil || copied.Status != operationresult.Success {
		return copied, false, err
	}
	s.deps.Bindings.SetCurrent(locked, target, row.ID)
	return copied, true, nil
}

// CopyBackground 从业务准入外调用；target 是副本归属，不代表来源访问者。
func (s *Service) CopyBackground(ctx context.Context, target share.Scope, req BackgroundCopyRequest) (operationresult.Result[*storage.Session], error) {
	var source *storage.Session
	var origin llm.Origin
	var pending []storage.NativeInput
	initial, err := func() (operationresult.Result[struct{}], error) {
		locked, release, err := s.deps.Bindings.EnterSessions(ctx, req.SourceSessionID)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		defer release()
		source, err = s.deps.Sessions.Get(locked, req.SourceSessionID)
		if errors.Is(err, storage.ErrNotFound) {
			return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: "来源会话不存在"}, nil
		}
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if !share.IsBackground(source) {
			return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "source session is not a background session"}, nil
		}
		if s.sessionBusy(source.ID) {
			return operationresult.Result[struct{}]{Status: operationresult.Busy, Message: "会话正忙"}, nil
		}
		origin, _, err = share.Origin(source)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		pending, err = s.deps.Dialogues.PendingInputs(locked, source.ID)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}()
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if initial.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: initial.Status, Message: initial.Message}, nil
	}
	prepared, err := s.prepareMaterial(ctx, origin, source, nil)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	idOrigin := share.BackgroundIDOrigin(target, req.TaskType, []string{target.Platform})
	nextID, err := s.deps.AllocateID(ctx, idOrigin)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	locked, release, err := s.deps.Bindings.EnterSessions(ctx, source.ID, nextID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	defer release()
	latest, err := s.deps.Sessions.Get(locked, source.ID)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.Session]{Status: operationresult.NotFound, Message: "来源会话不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if !share.IsBackground(latest) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "source session is not a background session"}, nil
	}
	if s.sessionBusy(source.ID) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Busy, Message: "会话正忙"}, nil
	}
	after, _, err := share.Origin(latest)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if origin != after {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "source session origin changed"}, nil
	}
	latestPending, err := s.deps.Dialogues.PendingInputs(locked, source.ID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	// 原生输入可在同一 ID 上补全工具结果；比较完整存储事实，不解释协议 JSON。
	if !slices.Equal(pending, latestPending) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "source pending inputs changed"}, nil
	}
	messages, err := s.deps.Messages.ListBySession(locked, source.ID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	toolCalls, err := s.deps.ToolCalls.ListBySession(locked, source.ID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	fields := make(storage.SessionMetadata, len(req.Metadata)+4)
	for key, value := range req.Metadata {
		fields[key] = value
	}
	if err := share.BackgroundMetadata(fields, req.TaskType); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
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
	title := strings.TrimSpace(req.Name)
	if title == "" {
		title = share.BackgroundTitle(req.TaskType, req.TaskID)
	}
	row := &storage.Session{ID: nextID, Owner: target.Owner, Platform: target.Platform, TaskType: req.TaskType, TaskID: req.TaskID, TaskName: req.Name,
		Mode: storage.SessionModeBackground, Title: title, Status: storage.SessionStatusActive, Metadata: metadata}
	return s.copySession(locked, materialCreate(row, source.ID, prepared.Data), messages, toolCalls)
}

func (s *Service) sessionBusy(id string) bool {
	for _, active := range s.deps.ActiveSessionIDs() {
		if active == id {
			return true
		}
	}
	return false
}

func (s *Service) copySession(ctx context.Context, request storage.SessionMaterialCreate, messages []storage.Message, toolCalls []storage.ToolCallRecord) (operationresult.Result[*storage.Session], error) {
	ids := make(map[string]string, len(messages))
	roles := make(map[string]string, len(messages))
	for _, message := range messages {
		id, err := s.deps.NewMessageID(ctx)
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		ids[message.ID], roles[message.ID] = id, message.Role
	}
	request.Messages = make([]*storage.Message, 0, len(messages))
	for _, message := range messages {
		fields, err := storage.DecodeSessionMetadata(message.Metadata)
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		if message.ToolBatchStatus == "pending" {
			return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "cannot copy an unfinished tool batch"}, nil
		}
		var resultIDs []string
		if raw, ok := fields[storage.ToolResultMessageKey]; ok {
			if err := json.Unmarshal(raw, &resultIDs); err != nil {
				return operationresult.Result[*storage.Session]{}, err
			}
		}
		for i, id := range resultIDs {
			mapped, ok := ids[id]
			if !ok || roles[id] != storage.RoleTool {
				return operationresult.Result[*storage.Session]{}, fmt.Errorf("message %s references missing or non-tool result %s", message.ID, id)
			}
			resultIDs[i] = mapped
		}
		copied := message
		if len(resultIDs) > 0 {
			if err := fields.Set(storage.ToolResultMessageKey, resultIDs); err != nil {
				return operationresult.Result[*storage.Session]{}, err
			}
			copied.Metadata, err = fields.Encode()
			if err != nil {
				return operationresult.Result[*storage.Session]{}, err
			}
		}
		copied.ID, copied.SessionID = ids[message.ID], request.Session.ID
		copied.ParentMessageID, copied.ReplyToMessageID, copied.ReplyToPlatformMessageID = "", "", ""
		request.Messages = append(request.Messages, &copied)
	}
	request.ToolCallRecords = make([]storage.ToolCallRecord, len(toolCalls))
	for i, record := range toolCalls {
		record.SessionID = request.Session.ID
		request.ToolCallRecords[i] = record
	}
	if err := s.deps.Sessions.CreateMaterial(ctx, request); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: request.Session}, nil
}

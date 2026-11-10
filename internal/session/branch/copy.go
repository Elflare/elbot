// 职责：创建后台会话副本并复制历史与材料。
// 重映射消息和工具结果关联，不继承前台绑定或运行中的执行状态。
package branch

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"elbot/internal/llm"
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
func (s *Service) CopyForeground(ctx context.Context, target share.Scope, sourceID string) (*storage.Session, error) {
	if !target.IsSuperadmin {
		return nil, share.ErrAccessDenied
	}
	source, err := s.deps.Sessions.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if source.Platform == target.Platform {
		return nil, nil
	}
	origin, _, err := share.Origin(source)
	if err != nil {
		return nil, err
	}
	prepared, err := s.prepareMaterial(ctx, origin, source, nil)
	if err != nil {
		return nil, err
	}
	idOrigin := share.ForegroundIDOrigin(target)
	nextID, err := s.deps.AllocateID(ctx, idOrigin)
	if err != nil {
		return nil, err
	}
	locked, release, err := s.deps.Bindings.EnterActivation(ctx, target, sourceID, nextID)
	if err != nil {
		return nil, err
	}
	defer release()
	current := s.deps.Bindings.CurrentBinding(target)
	if original, ok := share.BindingFromContext(ctx); ok && original != current {
		return nil, fmt.Errorf("session binding expired")
	}
	if current != nil {
		if err := s.requireIdle(current.SessionID()); err != nil {
			return nil, err
		}
	}
	if err := s.requireIdle(sourceID); err != nil {
		return nil, err
	}
	latest, err := s.deps.Sessions.Get(locked, sourceID)
	if err != nil {
		return nil, err
	}
	if latest.Platform == target.Platform {
		return nil, fmt.Errorf("source session platform changed")
	}
	latestOrigin, _, err := share.Origin(latest)
	if err != nil {
		return nil, err
	}
	if origin != latestOrigin {
		return nil, fmt.Errorf("source session origin changed")
	}
	messages, err := s.deps.Messages.ListBySession(locked, sourceID)
	if err != nil {
		return nil, err
	}
	fields, err := storage.DecodeSessionMetadata(latest.Metadata)
	if err != nil {
		return nil, err
	}
	delete(fields, "llm_checkpoint")
	delete(fields, "llm_seed")
	if err := idOrigin.Save(fields); err != nil {
		return nil, err
	}
	mode := latest.Mode
	if share.IsBackground(latest) {
		mode = storage.SessionModeWork
		if err := fields.Set("foreground_origin", map[string]any{
			"task_type": latest.TaskType, "task_id": latest.TaskID, "name": latest.TaskName,
			"owner": latest.Owner, "platform": latest.Platform,
		}); err != nil {
			return nil, err
		}
	}
	metadata, err := fields.Encode()
	if err != nil {
		return nil, err
	}
	row := &storage.Session{ID: nextID, ParentSessionID: sourceID, Owner: target.Owner, Platform: target.Platform,
		ChatType: target.ChatType, ChatID: target.ChatID, Mode: mode, Status: storage.SessionStatusActive,
		Title: latest.Title, Metadata: metadata}
	copies, err := s.copyMessages(ctx, row.ID, messages)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Sessions.CreateMaterial(locked, materialCreate(row, sourceID, prepared, copies)); err != nil {
		return nil, err
	}
	s.deps.Bindings.SetCurrent(locked, target, row.ID)
	return row, nil
}

// CopyBackground 从业务准入外调用；target 是副本归属，不代表来源访问者。
func (s *Service) CopyBackground(ctx context.Context, target share.Scope, req BackgroundCopyRequest) (*storage.Session, error) {
	var source *storage.Session
	var origin llm.Origin
	var pending []storage.NativeInput
	err := func() error {
		locked, release, err := s.deps.Bindings.EnterSessions(ctx, req.SourceSessionID)
		if err != nil {
			return err
		}
		defer release()
		source, err = s.deps.Sessions.Get(locked, req.SourceSessionID)
		if err != nil {
			return err
		}
		if share.WasPromoted(source) || !share.IsBackground(source) {
			return share.ErrForegroundSession
		}
		if err := s.requireIdle(source.ID); err != nil {
			return err
		}
		origin, _, err = share.Origin(source)
		if err != nil {
			return err
		}
		pending, err = s.deps.Dialogues.PendingInputs(locked, source.ID)
		return err
	}()
	if err != nil {
		return nil, err
	}
	prepared, err := s.prepareMaterial(ctx, origin, source, nil)
	if err != nil {
		return nil, err
	}
	idOrigin := share.BackgroundIDOrigin(target, req.TaskType, []string{target.Platform})
	nextID, err := s.deps.AllocateID(ctx, idOrigin)
	if err != nil {
		return nil, err
	}
	locked, release, err := s.deps.Bindings.EnterSessions(ctx, source.ID, nextID)
	if err != nil {
		return nil, err
	}
	defer release()
	latest, err := s.deps.Sessions.Get(locked, source.ID)
	if err != nil {
		return nil, err
	}
	if share.WasPromoted(latest) || !share.IsBackground(latest) {
		return nil, share.ErrForegroundSession
	}
	if err := s.requireIdle(source.ID); err != nil {
		return nil, err
	}
	after, _, err := share.Origin(latest)
	if err != nil {
		return nil, err
	}
	if origin != after {
		return nil, fmt.Errorf("source session origin changed")
	}
	latestPending, err := s.deps.Dialogues.PendingInputs(locked, source.ID)
	if err != nil {
		return nil, err
	}
	// 原生输入可在同一 ID 上补全工具结果；比较完整存储事实，不解释协议 JSON。
	if !slices.Equal(pending, latestPending) {
		return nil, fmt.Errorf("source pending inputs changed")
	}
	messages, err := s.deps.Messages.ListBySession(locked, source.ID)
	if err != nil {
		return nil, err
	}
	fields := make(storage.SessionMetadata, len(req.Metadata)+4)
	for key, value := range req.Metadata {
		fields[key] = value
	}
	if err := share.BackgroundMetadata(fields, req.TaskType); err != nil {
		return nil, err
	}
	if err := share.InheritOrigin(after, fields); err != nil {
		return nil, err
	}
	if err := idOrigin.Save(fields); err != nil {
		return nil, err
	}
	metadata, err := fields.Encode()
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Name)
	if title == "" {
		title = share.BackgroundTitle(req.TaskType, req.TaskID)
	}
	row := &storage.Session{ID: nextID, Owner: target.Owner, Platform: target.Platform, TaskType: req.TaskType, TaskID: req.TaskID, TaskName: req.Name,
		Mode: storage.SessionModeBackground, Title: title, Status: storage.SessionStatusActive, Metadata: metadata}
	copies, err := s.copyMessages(ctx, row.ID, messages)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Sessions.CreateMaterial(locked, materialCreate(row, source.ID, prepared, copies)); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Service) copyMessages(ctx context.Context, sessionID string, messages []storage.Message) ([]*storage.Message, error) {
	ids := make(map[string]string, len(messages))
	for _, message := range messages {
		id, err := s.deps.NewMessageID(ctx)
		if err != nil {
			return nil, err
		}
		ids[message.ID] = id
	}
	copies := make([]*storage.Message, 0, len(messages))
	for _, msg := range messages {
		fields, err := storage.DecodeSessionMetadata(msg.Metadata)
		if err != nil {
			return nil, err
		}
		if msg.ToolBatchStatus == "pending" {
			return nil, fmt.Errorf("cannot copy an unfinished tool batch")
		}
		var resultIDs []string
		if raw, ok := fields[storage.ToolResultMessageKey]; ok {
			if err := json.Unmarshal(raw, &resultIDs); err != nil {
				return nil, err
			}
		}
		for i, id := range resultIDs {
			if ids[id] == "" {
				return nil, fmt.Errorf("copied tool result is missing")
			}
			resultIDs[i] = ids[id]
		}
		if len(resultIDs) > 0 {
			if err := fields.Set(storage.ToolResultMessageKey, resultIDs); err != nil {
				return nil, err
			}
			msg.Metadata, err = fields.Encode()
			if err != nil {
				return nil, err
			}
		}
		msg.ID, msg.SessionID = ids[msg.ID], sessionID
		msg.ParentMessageID, msg.ReplyToMessageID, msg.ReplyToPlatformMessageID = "", "", ""
		copies = append(copies, &msg)
	}
	return copies, nil
}

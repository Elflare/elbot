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
	Kind            string                  // 副本后台来源种类。
	Name            string                  // 副本所属任务名称。
	Title           string                  // 副本标题；空值按种类和名称生成。
	Metadata        storage.SessionMetadata // 调用方的业务字段，不由本方法原地修改。
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
	nextID := storage.NewID()
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
	if err := share.BackgroundMetadata(fields, req.Kind, req.Name); err != nil {
		return nil, err
	}
	if err := share.InheritOrigin(after, fields); err != nil {
		return nil, err
	}
	metadata, err := fields.Encode()
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = share.BackgroundTitle(req.Kind, req.Name)
	}
	row := &storage.Session{ID: nextID, OwnerID: target.ActorID, Platform: target.Platform, PlatformScopeID: target.PlatformScopeID,
		Mode: storage.SessionModeBackground, Title: title, Status: storage.SessionStatusActive, Metadata: metadata}
	copies, err := copyMessages(row.ID, messages)
	if err != nil {
		return nil, err
	}
	if _, err := s.deps.Sessions.CreateMaterial(locked, materialCreate(row, source.ID, prepared, copies)); err != nil {
		return nil, err
	}
	return row, nil
}

func copyMessages(sessionID string, messages []storage.Message) ([]*storage.Message, error) {
	ids := make(map[string]string, len(messages))
	for _, message := range messages {
		ids[message.ID] = storage.NewID()
	}
	copies := make([]*storage.Message, 0, len(messages))
	for _, original := range messages {
		fields, err := storage.DecodeSessionMetadata(original.Metadata)
		if err != nil {
			return nil, err
		}
		if string(fields["tool_batch_status"]) == `"pending"` {
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
		msg := original
		if len(resultIDs) > 0 {
			if err := fields.Set(storage.ToolResultMessageKey, resultIDs); err != nil {
				return nil, err
			}
			msg.Metadata, err = fields.Encode()
			if err != nil {
				return nil, err
			}
		}
		msg.ID, msg.SessionID = ids[original.ID], sessionID
		msg.ParentMessageID, msg.ReplyToMessageID, msg.ReplyToPlatformMessageID = "", "", ""
		copies = append(copies, &msg)
	}
	return copies, nil
}

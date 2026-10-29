// 职责：按指定 assistant 消息及完整工具批次边界创建前台分支。
// 锁外准备材料，提交前复核来源和绑定，成功后激活新会话。
package branch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"elbot/internal/llm"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

// Fork 从业务准入外调用，材料准备期间不持有 Scope 或 Session 租约。
func (s *Service) Fork(ctx context.Context, scope share.Scope, fromMessageID string) (*storage.Session, error) {
	fromMessageID = strings.TrimSpace(fromMessageID)
	if fromMessageID == "" {
		return nil, fmt.Errorf("message id is required")
	}
	message, err := s.deps.Messages.Get(ctx, fromMessageID)
	if err != nil {
		return nil, err
	}
	sourceID := message.SessionID
	var source *storage.Session
	var original *share.Binding
	var origin llm.Origin
	var boundary string
	err = func() error {
		locked, release, err := s.deps.Bindings.EnterActivation(ctx, scope, sourceID)
		if err != nil {
			return err
		}
		defer release()
		original = s.deps.Bindings.CurrentBinding(scope)
		if binding, ok := share.BindingFromContext(ctx); ok && binding != original {
			return fmt.Errorf("session binding expired")
		}
		if original != nil {
			if err := s.requireIdle(original.SessionID()); err != nil {
				return err
			}
		}
		source, err = s.deps.Sessions.Get(locked, sourceID)
		if err != nil {
			return err
		}
		if err := checkForkAccess(scope, source); err != nil {
			return err
		}
		origin, _, err = share.Origin(source)
		if err != nil {
			return err
		}
		message, err = s.deps.Messages.Get(locked, fromMessageID)
		if err != nil {
			return err
		}
		boundary, err = s.forkBoundary(locked, sourceID, message)
		return err
	}()
	if err != nil {
		return nil, err
	}
	prepared, err := s.prepareMaterial(ctx, origin, source, message)
	if err != nil {
		return nil, err
	}
	nextID := storage.NewID()
	locked, release, err := s.deps.Bindings.EnterActivation(ctx, scope, sourceID, nextID)
	if err != nil {
		return nil, err
	}
	defer release()
	if s.deps.Bindings.CurrentBinding(scope) != original {
		return nil, fmt.Errorf("session binding expired")
	}
	if original != nil {
		if err := s.requireIdle(original.SessionID()); err != nil {
			return nil, err
		}
	}
	latest, err := s.deps.Sessions.Get(locked, sourceID)
	if err != nil {
		return nil, err
	}
	if err := checkForkAccess(scope, latest); err != nil {
		return nil, err
	}
	after, _, err := share.Origin(latest)
	if err != nil {
		return nil, err
	}
	if origin != after {
		return nil, fmt.Errorf("source session origin changed")
	}
	message, err = s.deps.Messages.Get(locked, fromMessageID)
	if err != nil {
		return nil, err
	}
	latestBoundary, err := s.forkBoundary(locked, sourceID, message)
	if err != nil {
		return nil, err
	}
	if boundary != latestBoundary {
		return nil, fmt.Errorf("source fork boundary changed")
	}
	fields := storage.SessionMetadata{}
	if err := share.InheritOrigin(after, fields); err != nil {
		return nil, err
	}
	metadata, err := fields.Encode()
	if err != nil {
		return nil, err
	}
	mode := latest.Mode
	if share.IsBackground(latest) {
		mode = storage.SessionModeWork
	}
	row := &storage.Session{ID: nextID, ParentSessionID: sourceID, ForkFromMessageID: boundary,
		OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID,
		Mode: mode, Status: storage.SessionStatusActive, Title: forkTitle(latest.Title), Metadata: metadata}
	if prepared == nil {
		err = s.deps.Sessions.Create(locked, row)
	} else {
		_, err = s.deps.Sessions.CreateMaterial(locked, materialCreate(row, sourceID, prepared, nil))
	}
	if err != nil {
		return nil, err
	}
	s.deps.Bindings.SetCurrent(locked, scope, row.ID)
	return row, nil
}

func checkForkAccess(scope share.Scope, row *storage.Session) error {
	allowed := false
	if scope.IsCLI {
		allowed = row.OwnerID == scope.ActorID
	} else {
		allowed = share.CanAccess(scope, row)
	}
	if !allowed {
		return fmt.Errorf("session %s is not in current user's accessible scope", row.ID)
	}
	return nil
}

func (s *Service) forkBoundary(ctx context.Context, sourceID string, message *storage.Message) (string, error) {
	if message.SessionID != sourceID || message.Role != storage.RoleAssistant {
		return "", fmt.Errorf("can only fork from a source assistant message")
	}
	fields, err := storage.DecodeSessionMetadata(message.Metadata)
	if err != nil {
		return "", err
	}
	if string(fields["tool_batch_status"]) == `"pending"` {
		return "", fmt.Errorf("cannot fork an unfinished tool batch")
	}
	var resultIDs []string
	if raw, ok := fields[storage.ToolResultMessageKey]; ok {
		if err := json.Unmarshal(raw, &resultIDs); err != nil {
			return "", err
		}
	}
	if len(resultIDs) == 0 {
		return message.ID, nil
	}
	result, err := s.deps.Messages.Get(ctx, resultIDs[len(resultIDs)-1])
	if err != nil {
		return "", err
	}
	if result.SessionID != sourceID || result.Role != storage.RoleTool {
		return "", fmt.Errorf("invalid tool batch fork boundary")
	}
	return result.ID, nil
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

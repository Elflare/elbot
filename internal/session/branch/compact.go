// 职责：根据已准备的压缩结果创建派生会话。
// 继承来源归属并原子保存材料，仅为前台来源切换当前绑定。
package branch

import (
	"context"

	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

type CompactedRequest struct {
	ID                   string              // 与源会话不同的预分配目标 ID。
	Title                string              // 压缩后标题。
	Metadata             string              // 上下文所有者准备的元数据 JSON。
	InitialMessage       string              // Chat 压缩生成的合成首条用户消息。
	Seed                 *storage.NativeSeed // 协议组件准备的原生根；无原生材料时为 nil。
	ExpectedCheckpointID string              // 创建事务需要复核的源游标。
}

func (s *Service) CreateCompacted(ctx context.Context, scope share.Scope, sourceID string, req CompactedRequest) (operationresult.Result[*storage.Session], error) {
	if req.ID == "" || req.ID == sourceID {
		return operationresult.Result[*storage.Session]{Status: operationresult.InvalidArguments, Message: "a distinct preallocated session ID is required"}, nil
	}
	source, err := s.deps.Sessions.Get(ctx, sourceID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	background := share.IsBackground(source)
	var release func()
	if background {
		ctx, release, err = s.deps.Bindings.EnterSessions(ctx, sourceID, req.ID)
	} else {
		ctx, release, err = s.deps.Bindings.EnterActivation(ctx, scope, sourceID, req.ID)
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	defer release()
	source, err = s.deps.Sessions.Get(ctx, sourceID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if background != share.IsBackground(source) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "session foreground state changed"}, nil
	}
	if background {
		// 后台压缩是原执行的交接，只匹配归属，不使用前台入口的访问豁免。
		if scope.Owner != source.Owner || scope.Platform != source.Platform || scope.ChatType != source.ChatType || scope.ChatID != source.ChatID || scope.TaskType != source.TaskType || scope.TaskID != source.TaskID {
			return operationresult.Result[*storage.Session]{Status: operationresult.Forbidden, Message: "session does not belong to current background execution"}, nil
		}
	} else {
		if !share.CanAccess(scope, source) {
			return operationresult.Result[*storage.Session]{Status: operationresult.Forbidden, Message: "session is not in current platform scope"}, nil
		}
		current := s.deps.Bindings.CurrentBinding(scope)
		if current == nil || current.SessionID() != sourceID {
			return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "session binding expired"}, nil
		}
		if original, ok := share.BindingFromContext(ctx); ok && original != current {
			return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "session binding expired"}, nil
		}
	}
	sourceFields, err := storage.DecodeSessionMetadata(source.Metadata)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	origin, _, err := share.OriginFromMetadata(sourceFields)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	fields, err := storage.DecodeSessionMetadata(req.Metadata)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if err := share.InheritOrigin(origin, fields); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	idOrigin, err := share.IDOriginFromMetadata(sourceFields)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if err := idOrigin.Save(fields); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if err := fields.Set("title_renamed", true); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if err := fields.Set("title_source", "compact"); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	metadata, err := fields.Encode()
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	row := &storage.Session{ID: req.ID, Owner: source.Owner, Platform: source.Platform,
		ChatID: source.ChatID, TaskType: source.TaskType, TaskID: source.TaskID, TaskName: source.TaskName, Title: req.Title, Mode: source.Mode,
		Status: storage.SessionStatusActive, Metadata: metadata, ChatType: source.ChatType,
	}
	var messages []*storage.Message
	if req.InitialMessage != "" {
		message := &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: req.InitialMessage}
		if err := storage.PrepareMessageID(ctx, s.deps.NewMessageID, message); err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		messages = append(messages, message)
	}
	if req.Seed == nil && len(messages) == 0 {
		err = s.deps.Sessions.Create(ctx, row)
	} else {
		err = s.deps.Sessions.CreateMaterial(ctx, storage.SessionMaterialCreate{Session: row, Messages: messages, Seed: req.Seed,
			SourceSessionID: sourceID, ExpectedCheckpointID: req.ExpectedCheckpointID})
	}
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if !background {
		s.deps.Bindings.SetCurrent(ctx, scope, row.ID)
	}
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: row}, nil
}

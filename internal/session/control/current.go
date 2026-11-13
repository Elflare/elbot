// 职责：实现当前会话的创建、获取、重置及模式激活与切换。
// 通过注入的绑定协调能力保证会话操作的准入与提交顺序。
package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) DefaultMode() string { return s.cfg.DefaultMode }

func (s *Service) GetOrCreateCurrent(ctx context.Context, scope share.Scope, firstMessage string) (operationresult.Result[*storage.Session], error) {
	ctx, release, err := s.deps.Bindings.EnterScope(ctx, scope)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	defer release()
	current, err := s.Current(ctx, scope)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if current.Status == operationresult.NoCurrentSession {
		return s.Create(ctx, scope, CreateRequest{Title: defaultTitle(firstMessage)})
	}
	return current, nil
}

func (s *Service) Create(ctx context.Context, scope share.Scope, req CreateRequest) (operationresult.Result[*storage.Session], error) {
	if req.Mode == "" {
		req.Mode = s.cfg.DefaultMode
	}
	if err := validateMode(req.Mode); err != nil {
		return operationresult.Result[*storage.Session]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	return s.createCurrent(ctx, scope, req)
}

// createCurrent 接收入口已确定并校验的模式，负责创建及绑定提交。
func (s *Service) createCurrent(ctx context.Context, scope share.Scope, req CreateRequest) (operationresult.Result[*storage.Session], error) {
	origin := share.ForegroundIDOrigin(scope)
	id, err := s.deps.AllocateID(ctx, origin)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	ctx, release, err := s.EnterActivation(ctx, scope, id)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	defer release()
	if s.currentBusy(scope, "") {
		return operationresult.Result[*storage.Session]{Status: operationresult.Busy, Message: "会话正忙"}, nil
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = "New session"
	}
	fields, err := storage.DecodeSessionMetadata(req.Metadata)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if err := origin.Save(fields); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	metadata, err := fields.Encode()
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	row := &storage.Session{ID: id, Owner: scope.Owner, Platform: scope.Platform,
		ChatType: scope.ChatType, ChatID: scope.ChatID, Mode: req.Mode, Status: storage.SessionStatusActive,
		Title: req.Title, Metadata: metadata}
	if err := s.deps.Sessions.Create(ctx, row); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	s.SetCurrent(ctx, scope, row.ID)
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: row}, nil
}

func (s *Service) Current(ctx context.Context, scope share.Scope) (operationresult.Result[*storage.Session], error) {
	bound, err := s.CurrentBound(ctx, scope)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if bound.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: bound.Status, Message: bound.Message}, nil
	}
	return operationresult.Result[*storage.Session]{Status: bound.Status, Message: bound.Message, Data: bound.Data.Session}, nil
}

type CurrentBoundData struct {
	Session *storage.Session
	Binding *share.Binding
}

func (s *Service) CurrentBound(ctx context.Context, scope share.Scope) (operationresult.Result[CurrentBoundData], error) {
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return operationresult.Result[CurrentBoundData]{}, err
	}
	defer release()
	binding := s.CurrentBinding(scope)
	if binding == nil {
		return operationresult.Result[CurrentBoundData]{Status: operationresult.NoCurrentSession, Message: "当前没有会话"}, nil
	}
	row, err := s.deps.Sessions.Get(ctx, binding.SessionID())
	if errors.Is(err, storage.ErrNotFound) {
		s.SetCurrent(ctx, scope, "")
		return operationresult.Result[CurrentBoundData]{Status: operationresult.NoCurrentSession, Message: "当前没有会话"}, nil
	}
	if err != nil {
		return operationresult.Result[CurrentBoundData]{}, err
	}
	return operationresult.Result[CurrentBoundData]{Status: operationresult.Success, Data: CurrentBoundData{Session: row, Binding: binding}}, nil
}

func (s *Service) ResetCurrent(ctx context.Context, scope share.Scope) (operationresult.Result[struct{}], error) {
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	defer release()
	if s.currentBusy(scope, "") {
		return operationresult.Result[struct{}]{Status: operationresult.Busy, Message: "会话正忙"}, nil
	}
	s.SetCurrent(ctx, scope, "")
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (s *Service) ActivateMode(ctx context.Context, scope share.Scope, req ActivateModeRequest) (operationresult.Result[ActivateModeResult], error) {
	if err := validateMode(req.Mode); err != nil {
		return operationresult.Result[ActivateModeResult]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	ctx, release, err := s.deps.Bindings.EnterScope(ctx, scope)
	if err != nil {
		return operationresult.Result[ActivateModeResult]{}, err
	}
	defer release()
	locked, leave, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return operationresult.Result[ActivateModeResult]{}, err
	}
	current, err := s.Current(locked, scope)
	if err != nil {
		return operationresult.Result[ActivateModeResult]{}, err
	}
	if current.Status == operationresult.NoCurrentSession {
		leave() // 释放旧 Session 准入，再创建新的目标。
		created, err := s.Create(ctx, scope, CreateRequest{Title: req.NewSessionTitle, Mode: req.Mode})
		if err != nil {
			return operationresult.Result[ActivateModeResult]{}, err
		}
		if created.Status != operationresult.Success {
			return operationresult.Result[ActivateModeResult]{Status: created.Status, Message: created.Message}, nil
		}
		return operationresult.Result[ActivateModeResult]{Status: operationresult.Success, Data: ActivateModeResult{Session: created.Data}}, nil
	}
	defer leave()
	row := current.Data
	if row.Mode == req.Mode {
		return operationresult.Result[ActivateModeResult]{Status: operationresult.Success, Data: ActivateModeResult{Session: row, AlreadyActive: true}}, nil
	}
	if s.sessionBusy(row.ID) {
		return operationresult.Result[ActivateModeResult]{Status: operationresult.Busy, Message: "会话正忙"}, nil
	}
	if req.Mode == storage.SessionModeChat {
		messages, err := s.deps.Messages.ListBySession(locked, row.ID)
		if err != nil {
			return operationresult.Result[ActivateModeResult]{}, err
		}
		if len(messages) != 0 {
			return operationresult.Result[ActivateModeResult]{Status: operationresult.Conflict, Message: "切换到 chat 模式需要空会话"}, nil
		}
	}
	row, err = s.deps.Sessions.Mutate(locked, row.ID, func(row *storage.Session) error {
		row.Mode, row.UpdatedAt = req.Mode, storage.Now()
		return nil
	})
	if err != nil {
		return operationresult.Result[ActivateModeResult]{}, err
	}
	return operationresult.Result[ActivateModeResult]{Status: operationresult.Success, Data: ActivateModeResult{Session: row}}, nil
}

// SetCurrent 的调用者持有 Scope 及源、目标 Session 准入。
// 绑定变化由 Binding 提交；Control 只维护与该激活对应的计时和退役状态。
func (s *Service) SetCurrent(ctx context.Context, scope share.Scope, id string) {
	old := s.CurrentBinding(scope)
	s.deps.Bindings.SetCurrent(ctx, scope, id)
	next := s.CurrentBinding(scope)
	if old == next {
		return
	}
	if old != nil {
		s.forgetBinding(old)
	}
	if next != nil {
		s.trackBinding(next)
	}
	if old != nil {
		s.releaseSessionIfIdle(old.SessionID())
	}
}

func (s *Service) currentBusy(scope share.Scope, target string) bool {
	if current := s.CurrentBinding(scope); current != nil && current.SessionID() != target {
		return s.sessionBusy(current.SessionID())
	}
	return false
}

func validateMode(mode string) error {
	if mode == storage.SessionModeWork || mode == storage.SessionModeChat {
		return nil
	}
	return fmt.Errorf("invalid session mode %q", mode)
}

func defaultTitle(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "New session"
	}
	if utf8.RuneCountInString(text) <= 40 {
		return text
	}
	return string([]rune(text)[:40]) + "..."
}

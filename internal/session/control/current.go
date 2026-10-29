// 职责：实现当前会话的创建、获取、重置及模式激活与切换。
// 通过注入的绑定协调能力保证会话操作的准入与提交顺序。
package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"elbot/internal/session/share"
	"elbot/internal/storage"
)

var ErrChatModeRequiresEmptySession = errors.New("chat mode requires an empty work session")

func (s *Service) DefaultMode() string { return s.cfg.DefaultMode }

func (s *Service) GetOrCreateCurrent(ctx context.Context, scope share.Scope, firstMessage string) (*storage.Session, error) {
	// 先只持有 Scope；确认需要创建后才一次取得全部 Session 准入。
	ctx, release, err := s.deps.Bindings.EnterScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer release()
	row, err := s.Current(ctx, scope)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	return s.Create(ctx, scope, CreateRequest{Title: defaultTitle(firstMessage)})
}

func (s *Service) Create(ctx context.Context, scope share.Scope, req CreateRequest) (*storage.Session, error) {
	if req.Mode == "" {
		req.Mode = s.cfg.DefaultMode
	}
	if err := validateMode(req.Mode); err != nil {
		return nil, err
	}
	return s.createCurrent(ctx, scope, req)
}

// createCurrent 接收入口已确定并校验的模式，负责创建及绑定提交。
func (s *Service) createCurrent(ctx context.Context, scope share.Scope, req CreateRequest) (*storage.Session, error) {
	if req.ID == "" {
		req.ID = storage.NewID()
	}
	ctx, release, err := s.EnterActivation(ctx, scope, req.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.canReplaceCurrent(scope, ""); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = "New session"
	}
	row := &storage.Session{ID: req.ID, OwnerID: scope.ActorID, Platform: scope.Platform,
		PlatformScopeID: scope.PlatformScopeID, Mode: req.Mode, Status: storage.SessionStatusActive,
		Title: req.Title, Metadata: req.Metadata}
	if err := s.deps.Sessions.Create(ctx, row); err != nil {
		return nil, err
	}
	s.SetCurrent(ctx, scope, row.ID)
	return row, nil
}

func (s *Service) Current(ctx context.Context, scope share.Scope) (*storage.Session, error) {
	row, _, err := s.CurrentBound(ctx, scope)
	return row, err
}

func (s *Service) CurrentBound(ctx context.Context, scope share.Scope) (*storage.Session, *share.Binding, error) {
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	binding := s.CurrentBinding(scope)
	if binding == nil {
		return nil, nil, storage.ErrNotFound
	}
	row, err := s.deps.Sessions.Get(ctx, binding.SessionID())
	if errors.Is(err, storage.ErrNotFound) {
		s.SetCurrent(ctx, scope, "")
	}
	return row, binding, err
}

func (s *Service) ResetCurrent(ctx context.Context, scope share.Scope) error {
	ctx, release, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return err
	}
	defer release()
	if err := s.canReplaceCurrent(scope, ""); err != nil {
		return err
	}
	s.SetCurrent(ctx, scope, "")
	return nil
}

func (s *Service) ActivateMode(ctx context.Context, scope share.Scope, req ActivateModeRequest) (ActivateModeResult, error) {
	if err := validateMode(req.Mode); err != nil {
		return ActivateModeResult{}, err
	}
	ctx, release, err := s.deps.Bindings.EnterScope(ctx, scope)
	if err != nil {
		return ActivateModeResult{}, err
	}
	defer release()
	if s.CurrentBinding(scope) == nil {
		row, err := s.createCurrent(ctx, scope, CreateRequest{Title: req.NewSessionTitle, Mode: req.Mode})
		return ActivateModeResult{Session: row}, err
	}
	locked, leave, err := s.EnterActivation(ctx, scope)
	if err != nil {
		return ActivateModeResult{}, err
	}
	row, err := s.Current(locked, scope)
	if errors.Is(err, storage.ErrNotFound) {
		leave() // 释放旧 Session 准入，再创建新的目标。
		row, err = s.createCurrent(ctx, scope, CreateRequest{Title: req.NewSessionTitle, Mode: req.Mode})
		return ActivateModeResult{Session: row}, err
	}
	defer leave()
	if err != nil {
		return ActivateModeResult{}, err
	}
	if row.Mode == req.Mode {
		return ActivateModeResult{Session: row, AlreadyActive: true}, nil
	}
	if err := s.requireIdle(row.ID); err != nil {
		return ActivateModeResult{}, err
	}
	if req.Mode == storage.SessionModeChat {
		messages, err := s.deps.Messages.ListBySession(locked, row.ID)
		if err != nil {
			return ActivateModeResult{}, err
		}
		if len(messages) != 0 {
			return ActivateModeResult{}, ErrChatModeRequiresEmptySession
		}
	}
	row, err = s.deps.Sessions.Mutate(locked, row.ID, func(row *storage.Session) error {
		row.Mode, row.UpdatedAt = req.Mode, storage.Now()
		return nil
	})
	return ActivateModeResult{Session: row}, err
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

func (s *Service) canReplaceCurrent(scope share.Scope, target string) error {
	if current := s.CurrentBinding(scope); current != nil && current.SessionID() != target {
		return s.requireIdle(current.SessionID())
	}
	return nil
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

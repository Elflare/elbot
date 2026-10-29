// 职责：管理绑定局部定时器、活动记录、同步退役与持久化历史清理。
// 闲置时钟独立于 UpdatedAt，不扫描；忙碌后由 SessionIdle 重新启用计时。
package control

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
	"elbot/internal/signal"
)

type IdleExpirationConfig struct {
	GroupUserTTLMinutes         int // 群聊／频道普通用户期限，分钟；非正值禁用。
	GroupSuperadminTTLMinutes   int // 群聊／频道超级管理员期限。
	PrivateUserTTLMinutes       int // 其他会话普通用户期限。
	PrivateSuperadminTTLMinutes int // 其他会话超级管理员期限。
}

func (c IdleExpirationConfig) TTLMinutes(scope share.Scope, superadmin bool) int {
	if scope.ConversationKind == contextinfo.ConversationGroup || scope.ConversationKind == contextinfo.ConversationChannel {
		if superadmin {
			return c.GroupSuperadminTTLMinutes
		}
		return c.GroupUserTTLMinutes
	}
	if superadmin {
		return c.PrivateSuperadminTTLMinutes
	}
	return c.PrivateUserTTLMinutes
}

type idleBinding struct {
	ttl      time.Duration // 激活时按范围及权限取得的期限；禁用时仍跟踪绑定引用。
	deadline time.Time     // 零值表示忙碌，等待 SessionIdle；不读取持久化更新时间。
	version  uint64        // 每次调度或撤销递增，拒绝迟到回调。
	timer    *time.Timer   // 当前排期；旧回调可能仍在退出，另由 timers 计数。
}

func (s *Service) trackBinding(binding *share.Binding) {
	ttl := time.Duration(s.cfg.IdleExpiration.TTLMinutes(binding.Scope(), s.deps.IsSuperadmin(binding.Scope()))) * time.Minute
	state := &idleBinding{ttl: ttl}
	if s.requireIdle(binding.SessionID()) == nil {
		state.deadline = time.Now().Add(ttl)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bindings := s.idle[binding.SessionID()]
	if bindings == nil {
		bindings = make(map[*share.Binding]*idleBinding)
		s.idle[binding.SessionID()] = bindings
	}
	bindings[binding] = state
	s.scheduleLocked(binding, state)
}

func (s *Service) stopTimerLocked(state *idleBinding) {
	state.version++
	if state.timer != nil {
		if state.timer.Stop() {
			s.timers.Done()
		}
		state.timer = nil
	}
}

func (s *Service) scheduleLocked(binding *share.Binding, state *idleBinding) {
	s.stopTimerLocked(state)
	if !s.started || s.closed || state.ttl <= 0 || state.deadline.IsZero() {
		return
	}
	version := state.version
	s.timers.Add(1)
	state.timer = time.AfterFunc(time.Until(state.deadline), func() {
		defer s.timers.Done()
		s.expireBinding(binding, version)
	})
}

func (s *Service) expireBinding(binding *share.Binding, version uint64) {
	ctx, release, err := s.deps.Bindings.EnterBinding(s.runCtx, binding)
	if err != nil {
		if s.runCtx.Err() == nil && binding.Valid() {
			slog.WarnContext(s.runCtx, "session idle admission failed", "session_id", binding.SessionID(), "error", err)
		}
		return
	}
	defer release()
	s.mu.Lock()
	state := s.idle[binding.SessionID()][binding]
	if s.closed || state == nil || state.version != version {
		s.mu.Unlock()
		return
	}
	state.timer = nil // 当前回调的退出由 defer 计数，不再由 Stop 处理。
	s.mu.Unlock()
	if s.requireIdle(binding.SessionID()) != nil {
		s.mu.Lock()
		state.deadline = time.Time{}
		s.mu.Unlock()
		return
	}
	// 准入保护 deadline 不会被活动更新；时钟提前唤醒时只补排剩余期限。
	s.mu.Lock()
	if time.Now().Before(state.deadline) {
		s.scheduleLocked(binding, state)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.SetCurrent(ctx, binding.Scope(), "")
	slog.InfoContext(ctx, "session idle expired", "session_id", binding.SessionID(), "binding_version", binding.Version())
}

// RecordActivity 只接受原激活句柄；调用者先判断输入确实属于有效活动。
func (s *Service) RecordActivity(ctx context.Context, binding *share.Binding) error {
	_, release, err := s.deps.Bindings.EnterBinding(ctx, binding)
	if err != nil {
		return err
	}
	defer release()
	return s.refreshBinding(binding)
}

func (s *Service) refreshBinding(binding *share.Binding) error {
	busy := s.requireIdle(binding.SessionID()) != nil
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return signal.ErrClosed
	}
	state := s.idle[binding.SessionID()][binding]
	if state == nil {
		return fmt.Errorf("binding was not activated through session control")
	}
	state.deadline = time.Time{}
	if !busy {
		state.deadline = time.Now().Add(state.ttl)
	}
	s.scheduleLocked(binding, state)
	return nil
}

// SessionIdle 在实际执行退出后、业务准入外调用；取消执行的收尾调用须使用独立的清理 context。
// 没有绑定的会话也可能仍有执行缓存需要释放。
func (s *Service) SessionIdle(ctx context.Context, id string) error {
	_, release, err := s.EnterSessions(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if err := s.requireIdle(id); err != nil {
		return nil
	}
	s.mu.Lock()
	bindings := s.idle[id]
	if len(bindings) == 0 {
		s.mu.Unlock()
		s.deps.ReleaseSession(id)
		return nil
	}
	defer s.mu.Unlock()
	if s.closed {
		return signal.ErrClosed
	}
	// Session 准入保护关联绑定；释放前统一续期并失效旧回调，无需倒取 Scope。
	now := time.Now()
	for binding, state := range bindings {
		state.deadline = now.Add(state.ttl)
		s.scheduleLocked(binding, state)
	}
	return nil
}

func (s *Service) forgetBinding(binding *share.Binding) {
	s.mu.Lock()
	bindings := s.idle[binding.SessionID()]
	if state := bindings[binding]; state != nil {
		s.stopTimerLocked(state)
		delete(bindings, binding)
		if len(bindings) == 0 {
			delete(s.idle, binding.SessionID())
		}
	}
	s.mu.Unlock()
	s.deps.ReleaseBinding(binding)
}

// 调用者持有该 Session 准入，保证最后一个绑定离开与新激活不能交错。
func (s *Service) releaseSessionIfIdle(id string) {
	s.mu.Lock()
	bound := len(s.idle[id]) != 0
	s.mu.Unlock()
	if !bound && s.requireIdle(id) == nil {
		s.deps.ReleaseSession(id)
	}
}

func (s *Service) clearSessionBindings(ctx context.Context, id string) {
	s.mu.Lock()
	bindings := make([]*share.Binding, 0, len(s.idle[id]))
	for binding := range s.idle[id] {
		bindings = append(bindings, binding)
	}
	s.mu.Unlock()
	s.deps.Bindings.ClearSessionBindings(ctx, id)
	for _, binding := range bindings {
		s.forgetBinding(binding)
	}
	s.releaseSessionIfIdle(id)
}

func (s *Service) CleanupExpired(ctx context.Context, cutoff time.Time) (int, error) {
	ids, err := s.deps.Sessions.ListExpiredIDs(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		locked, release, err := s.EnterSessions(ctx, id)
		if err != nil {
			return count, err
		}
		if s.requireIdle(id) != nil {
			release()
			continue
		}
		deleted, err := s.deps.Sessions.DeleteIfExpired(locked, id, cutoff)
		if err == nil && deleted {
			s.clearSessionBindings(locked, id)
			count++
		}
		release()
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

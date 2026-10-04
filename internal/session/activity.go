package session

import "errors"

var ErrSessionBusy = errors.New("当前会话处理中，暂不支持切换。如有必要，请先使用 /stop 结束当前处理。")

// SetActivitySource is wired before inputs or maintenance start. Session owns
// activation, while execution state remains owned by Request/Turn.
func (s *Service) SetActivitySource(active func() []string) { s.activeSessionIDs = active }

func (s *Service) activeIDs() []string {
	if s.activeSessionIDs == nil {
		return nil
	}
	return s.activeSessionIDs()
}

func (s *Service) requireIdle(id string) error {
	for _, active := range s.activeIDs() {
		if active == id {
			return ErrSessionBusy
		}
	}
	return nil
}

func (s *Service) canReplaceCurrent(scope Scope, targetID string) error {
	s.mu.Lock()
	current := s.current[scope.Key()]
	s.mu.Unlock()
	if current == nil || current.SessionID() == targetID {
		return nil
	}
	return s.requireIdle(current.SessionID())
}

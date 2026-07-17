package commands

import (
	"sync"

	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
)

type SessionCommandState struct {
	mu            sync.RWMutex
	ids           map[sessionpkg.Scope][]string
	listPageSize  int
	retentionDays int
}

func NewSessionCommandState(listPageSize, retentionDays int) *SessionCommandState {
	return &SessionCommandState{ids: map[sessionpkg.Scope][]string{}, listPageSize: listPageSize, retentionDays: retentionDays}
}

func (s *SessionCommandState) set(scope sessionpkg.Scope, sessions []storage.SessionSummary) {
	if s == nil {
		return
	}
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	s.mu.Lock()
	s.ids[scope] = ids
	s.mu.Unlock()
}

func (s *SessionCommandState) get(scope sessionpkg.Scope) []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	ids := append([]string(nil), s.ids[scope]...)
	s.mu.RUnlock()
	return ids
}

func (s *SessionCommandState) SetListPageSize(size int) {
	if s == nil || size <= 0 {
		return
	}
	s.mu.Lock()
	s.listPageSize = size
	s.mu.Unlock()
}

func (s *SessionCommandState) SetRetentionDays(days int) {
	if s == nil || days <= 0 {
		return
	}
	s.mu.Lock()
	s.retentionDays = days
	s.mu.Unlock()
}

func (s *SessionCommandState) config() (int, int) {
	if s == nil {
		return 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listPageSize, s.retentionDays
}

type SessionModule struct{}

func (SessionModule) RegisterCommands(registrar Registrar, deps Deps) error {
	if deps.SessionState == nil {
		deps.SessionState = NewSessionCommandState(defaultSessionListPageSize, 30)
	}
	return RegisterFactories(registrar, deps,
		NewNew,
		NewStatus,
		NewSessions,
		NewArchives,
		NewArchive,
		NewUnarchive,
		NewPin,
		NewUnpin,
		NewRename,
		NewDelete,
		NewClean,
		NewResume,
		NewMessages,
		NewFork,
		NewWork,
		NewChat,
	)
}

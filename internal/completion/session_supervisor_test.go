package completion

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"testing"
	"time"
)

type sessionTestModels struct{}

func (sessionTestModels) ResolveNaming(context.Context) modelmgr.NamingSelection {
	return modelmgr.NamingSelection{}
}

type sessionTestMaterials struct{}

func (sessionTestMaterials) MaterialFor(llm.Origin) (session.MaterialPreparer, error) {
	return session.DisplayMaterial{}, nil
}

// newSessionSupervisor uses the real Session lifecycle; each test owns its store.
func newSessionSupervisor(t *testing.T, store storage.Store, configure ...func(*session.SupervisorDeps)) *session.Supervisor {
	t.Helper()
	cfg := &config.Config{Session: config.SessionConfig{DefaultMode: storage.SessionModeWork, Naming: config.SessionNamingConfig{TriggerStep: 100000}}}
	deps := session.SupervisorDeps{Store: store, Models: sessionTestModels{}, Materials: sessionTestMaterials{},
		ActiveSessionIDs: func() []string { return nil }, CheckForeground: func(context.Context, *storage.Session) error { return nil },
		IsSuperadmin: func(session.Scope) bool { return false }, ReleaseSession: func(string) {}, ReleaseBinding: func(*session.Binding) {}}
	for _, configure := range configure {
		configure(&deps)
	}
	s, err := session.NewSupervisor(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	return s
}

package input

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	toolregistry "elbot/internal/tool/registry"
	toolstate "elbot/internal/tool/state"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type activityTestHooks struct{}

func (activityTestHooks) AgentInputPrepared(_ context.Context, _ agentshare.Session, message agentshare.Message) ([]llm.MessageSegment, events.LogResult, error) {
	return message.Segments, events.LogResult{}, nil
}

type activityTestOutput struct{}

func (activityTestOutput) SendChat(context.Context, string) error     { return nil }
func (activityTestOutput) Notify(context.Context, slog.Level, string) {}

type activityTestPreloader struct{}

func (activityTestPreloader) PrepareTools(_ context.Context, names []string) (tool.ToolPreload, error) {
	return tool.ToolPreload{Accepted: []bool{true}, Update: tool.StateUpdate{Tools: []tool.DiscoveredTool{{Name: "alpha", CanonicalName: "alpha", Source: tool.SourceKindNative}}}}, nil
}

func (activityTestPreloader) PrepareSkills(_ context.Context, _ string, names []string) (tool.SkillPreload, error) {
	return tool.SkillPreload{Names: names, Accepted: []bool{true}, Update: tool.StateUpdate{Tools: []tool.DiscoveredTool{{Name: "wrapper", CanonicalName: "wrapper", Source: tool.SourceKindNative}}}}, nil
}

func TestSuccessfulPureDirectivesRenewOriginalBinding(t *testing.T) {
	for _, tc := range []struct {
		name, input     string
		existing, renew bool
	}{
		{"new tool", "@tool:alpha", false, true},
		{"existing tool", "@tool:alpha", true, true},
		{"new skill wrapper", "@skill:doc", false, true},
		{"existing skill wrapper", "@skill:doc", true, true},
		{"empty input", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			synctest.Test(t, func(t *testing.T) {
				cfg := &config.Config{Session: config.SessionConfig{DefaultMode: storage.SessionModeWork, Naming: config.SessionNamingConfig{TriggerStep: 100000}, IdleExpiration: config.SessionIdleExpirationConfig{PrivateUserTTLMinutes: 1, PrivateSuperadminTTLMinutes: 1}}}
				sessions, err := session.NewSupervisor(t.Context(), cfg, session.SupervisorDeps{
					Store: store, Models: sessionTestModels{}, Materials: sessionTestMaterials{},
					ActiveSessionIDs: func() []string { return nil }, CheckForeground: func(context.Context, *storage.Session) error { return nil },
					IsSuperadmin: func(session.Scope) bool { return false }, ReleaseSession: func(string) {}, ReleaseBinding: func(*session.Binding) {},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer sessions.Close(context.Background())
				if err := sessions.Start(); err != nil {
					t.Fatal(err)
				}
				identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: security.DefaultPermission()})
				row, err := sessions.Create(t.Context(), identity.Scope(t.Context()), session.CreateRequest{})
				if err != nil {
					t.Fatal(err)
				}
				_, binding, err := sessions.CurrentBound(t.Context(), identity.Scope(t.Context()))
				if err != nil {
					t.Fatal(err)
				}
				ctx := session.WithBinding(t.Context(), binding)
				coordinator := NewInputCoordinator(InputCoordinatorDeps{
					Sessions: sessions, SessionRows: store.Sessions(), Identity: identity,
					Execution: directiveExecution{}, Turns: turn.NewManager(), Hooks: activityTestHooks{}, Output: activityTestOutput{},
					Preloader: activityTestPreloader{}, ToolState: directiveState{toolstate.NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools)},
					NewWorkspaceStore: func(id string) workspace.WorkspaceStore { return sessions.WorkspaceStore(id) },
				})
				if tc.existing {
					if err := coordinator.handleSessionInput(ctx, row, tc.input); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(59 * time.Second)
				if err := coordinator.handleSessionInput(ctx, row, tc.input); err != nil {
					t.Fatal(err)
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				if binding.Valid() != tc.renew {
					t.Fatalf("binding valid=%v, expected renewal=%v", binding.Valid(), tc.renew)
				}
				if tc.renew {
					time.Sleep(58 * time.Second)
					synctest.Wait()
					if binding.Valid() {
						t.Fatal("renewed binding did not expire after a full idle period")
					}
				}
			})
		})
	}
}

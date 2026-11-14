package input

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/testutil"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
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

func (activityTestHooks) AgentInputPrepared(_ context.Context, _ agentshare.Session, message agentshare.Message) ([]llm.MessageSegment, error) {
	return message.Segments, nil
}

type activityTestOutput struct{}

func (activityTestOutput) Send(context.Context, delivery.StreamEvent) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}
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
					SessionIDCode: func(name string, _ contextinfo.ChatType) (string, error) { return name, nil },
					Store:         store, Models: sessionTestModels{}, Materials: sessionTestMaterials{},
					ActiveSessionIDs: func() []string { return nil }, CheckForeground: func(context.Context, *storage.Session) (operationresult.Result[struct{}], error) {
						return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
					},
					IsSuperadmin: func(session.Scope) bool { return false }, ReleaseSession: func(string) {}, ReleaseBinding: func(*session.Binding) {},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer sessions.Close(context.Background())
				if err := sessions.Start(); err != nil {
					t.Fatal(err)
				}
				identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
				created, err := sessions.Create(t.Context(), identity.Scope(testutil.WithCLIConversation(t.Context())), session.CreateRequest{})
				if err != nil || created.Status != operationresult.Success {
					t.Fatalf("create session: %+v %v", created, err)
				}
				row := created.Data
				current, err := sessions.CurrentBound(t.Context(), identity.Scope(testutil.WithCLIConversation(t.Context())))
				if err != nil || current.Status != operationresult.Success {
					t.Fatalf("current: %+v %v", current, err)
				}
				binding := current.Data.Binding
				ctx := session.WithBinding(testutil.WithCLIConversation(t.Context()), binding)
				coordinator := NewInputCoordinator(InputCoordinatorDeps{CommandText: testCommandText,
					Sessions: sessions, Identity: identity,
					Execution: directiveExecution{}, Turns: turn.NewSupervisor(testutil.IDSequence()), Hooks: activityTestHooks{}, Output: activityTestOutput{},
					Preloader: activityTestPreloader{}, ToolState: directiveState{toolstate.NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools)},
					NewWorkspaceStore: func(id string) workspace.WorkspaceStore { return sessions.WorkspaceStore(id) },
				})
				if tc.existing {
					if _, err := coordinator.handleSessionInput(ctx, row, tc.input); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(59 * time.Second)
				if _, err := coordinator.handleSessionInput(ctx, row, tc.input); err != nil {
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

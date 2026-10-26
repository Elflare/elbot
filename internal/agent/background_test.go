package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

func newBackgroundAgent(t *testing.T) (*Supervisor, SupervisorDeps, *tool.Supervisor, context.Context) {
	t.Helper()
	deps := isolatedModuleDeps(t)
	cfg := isolatedAgentConfig()
	root := t.TempDir()
	cfg.ConfigPath = filepath.Join(root, "config.toml")
	cfg.Sandbox.Root = root
	skillDir := filepath.Join(root, "skills", "agent", "task_notes")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: task_notes
description: Task guidance
---

KEEP_BACKGROUND_GUIDANCE`), 0600); err != nil {
		t.Fatal(err)
	}
	tools, err := tool.NewSupervisor(t.Context(), cfg, tool.SupervisorDeps{
		Store: deps.Store, Models: deps.Models, Media: deps.Media, Permission: deps.Permission,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tools.Close(context.Background()) })
	if err := tools.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	deps.Tools = tools
	m, err := NewSupervisor(t.Context(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx := workspace.WithPathScope(t.Context(), workspace.PathScope{Dir: root, Restricted: true})
	ctx = contextinfo.WithBackgroundKind(ctx, contextinfo.BackgroundKindCron)
	return m, deps, tools, ctx
}

func TestBackgroundPublicRunPreloadContinuationAndCopy(t *testing.T) {
	m, deps, tools, ctx := newBackgroundAgent(t)
	req := BackgroundRequest{
		Kind: "cron", Name: "report", Prompt: "report",
		Actor:         contextinfo.Actor{ID: "cli:local", Platform: "cli"},
		ToolListNames: []string{"task_notes"},
		ELwispTools:   []tool.ELwispToolDeclaration{{Name: "remote", CanonicalName: "event_remote", EventKey: "event", Endpoint: "https://example.invalid"}},
	}
	result, err := m.RunBackground(ctx, req)
	if err != nil || result.SessionID == "" || result.RunID == "" || result.MessageID == "" || result.Text != "isolated answer" || result.Outcome != "completed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	row, err := deps.Store.Sessions().Get(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := tools.DecodeState(row.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, declaration := range before.DiscoveredTools {
		if declaration.CanonicalName == "event_remote" && declaration.EventKey == "event" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing committed declaration: %+v", before)
	}
	messages, err := deps.Store.Messages().ListBySession(ctx, row.ID)
	if err != nil || len(messages) != 2 || !strings.Contains(messages[0].Content, "KEEP_BACKGROUND_GUIDANCE") || messages[1].ID != result.MessageID {
		t.Fatalf("history=%+v err=%v", messages, err)
	}
	saved := session.NewWorkspaceStore(m.sessions, deps.Store.Sessions(), row.ID)
	dir, err := saved.GetWorkspaceDir(ctx)
	path, _ := workspace.PathScopeFromContext(ctx)
	if err != nil || dir != path.Dir {
		t.Fatalf("workspace=%q err=%v", dir, err)
	}
	req.SessionID = row.ID
	req.ToolListNames = []string{"missing_skill"}
	req.ELwispTools = []tool.ELwispToolDeclaration{{Name: "changed", CanonicalName: "changed"}}
	req.Prompt = "continue"
	continued, err := m.RunBackground(ctx, req)
	if err != nil || continued.SessionID != row.ID {
		t.Fatalf("continuation=%+v err=%v", continued, err)
	}
	latest, err := deps.Store.Sessions().Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := tools.DecodeState(latest.Metadata)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("changed declarations: before=%+v after=%+v err=%v", before, after, err)
	}

	foregroundScope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	foreground, err := m.sessions.Create(ctx, foregroundScope, session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]json.RawMessage{
		"cron_broadcast_copy": json.RawMessage("true"),
		"sequence":            json.RawMessage("9007199254740993"),
		"nested":              json.RawMessage(`{"keep":true}`),
	}
	copyID, err := m.CopyBackground(ctx, BackgroundCopyRequest{
		SourceSessionID: row.ID, Scope: foregroundScope, Kind: "cron", Name: "copy", Metadata: metadata,
	})
	if err != nil || copyID == row.ID {
		t.Fatalf("copy=%q err=%v", copyID, err)
	}
	copied, err := deps.Store.Sessions().Get(ctx, copyID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := storage.DecodeSessionMetadata(copied.Metadata)
	if err != nil || string(fields["cron_broadcast_copy"]) != "true" || string(fields["sequence"]) != "9007199254740993" || string(fields["nested"]) != `{"keep":true}` {
		t.Fatalf("metadata=%s err=%v", copied.Metadata, err)
	}
	copyState, err := tools.DecodeState(copied.Metadata)
	if err != nil || len(copyState.DiscoveredTools) != 0 {
		t.Fatalf("copied execution state=%+v err=%v", copyState, err)
	}
	current, err := m.sessions.Current(ctx, foregroundScope)
	if err != nil || current.ID != foreground.ID {
		t.Fatalf("foreground=%+v err=%v", current, err)
	}
	if _, err := m.CopyBackground(ctx, BackgroundCopyRequest{SourceSessionID: foreground.ID, Scope: foregroundScope}); !errors.Is(err, session.ErrForegroundSession) {
		t.Fatalf("foreground source accepted: %v", err)
	}
	if _, err := m.CopyBackground(ctx, BackgroundCopyRequest{SourceSessionID: row.ID, Scope: foregroundScope, Metadata: map[string]json.RawMessage{"bad": json.RawMessage("invalid")}}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := m.sessions.Resume(ctx, foregroundScope, row.ID); err != nil {
		t.Fatal(err)
	}
	taken, err := m.RunBackground(ctx, req)
	if err != nil || !taken.TakenOver || taken.Outcome != "taken_over" {
		t.Fatalf("takeover=%+v err=%v", taken, err)
	}
}

type backgroundBlockingTools struct {
	ToolCapabilities
	entered chan struct{}
}

func (b *backgroundBlockingTools) PrepareBackground(ctx context.Context, _ tool.BackgroundPreloadRequest) (tool.BackgroundPreload, error) {
	close(b.entered)
	<-ctx.Done()
	return tool.BackgroundPreload{}, ctx.Err()
}

func TestBackgroundCancellationDuringPreparation(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "agent"}[shutdown], func(t *testing.T) {
			deps := isolatedModuleDeps(t)
			blocking := &backgroundBlockingTools{ToolCapabilities: deps.Tools, entered: make(chan struct{})}
			deps.Tools = blocking
			m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			ctx := workspace.WithPathScope(t.Context(), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			results := make(chan error, 1)
			go func() {
				result, err := m.RunBackground(ctx, BackgroundRequest{Kind: "cron", Name: "cancel", Prompt: "run"})
				if result.SessionID == "" {
					results <- errors.New("lost prepared session ID")
					return
				}
				results <- err
			}()
			select {
			case <-blocking.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("preparation never began")
			}
			if shutdown {
				m.BeginClose()
			} else {
				cancel()
			}
			select {
			case err := <-results:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("preparation ignored cancellation")
			}
			rows, err := deps.Store.Sessions().List(t.Context(), storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 10})
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			messages, err := deps.Store.Messages().ListBySession(t.Context(), rows[0].ID)
			if err != nil || len(messages) != 0 {
				t.Fatalf("executed after preload cancellation: %+v %v", messages, err)
			}
			if shutdown {
				if _, err := m.RunBackground(t.Context(), BackgroundRequest{}); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if _, err := m.CopyBackground(t.Context(), BackgroundCopyRequest{}); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if _, err := m.CleanupSessions(t.Context(), time.Now()); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestBackgroundCleanupSkipsBusySessions(t *testing.T) {
	m, deps, _, ctx := newBackgroundAgent(t)
	idle, err := m.sessions.PrepareBackground(ctx, session.Scope{}, session.BackgroundRequest{Kind: "cron", Name: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	busy, err := m.sessions.PrepareBackground(ctx, session.Scope{}, session.BackgroundRequest{Kind: "cron", Name: "busy"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.turns.StartExecution(busy.ID, turn.Input{Text: "work"}, turn.NewExecution("busy"), "attempt") {
		t.Fatal("could not mark busy")
	}
	defer m.turns.StopSession(busy.ID)
	count, err := m.CleanupSessions(ctx, time.Now().Add(time.Hour))
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := deps.Store.Sessions().Get(ctx, idle.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := deps.Store.Sessions().Get(ctx, busy.ID); err != nil {
		t.Fatal(err)
	}
}

type backgroundBlockingClient struct {
	isolatedClient
	entered chan struct{}
}

func (c backgroundBlockingClient) Stream(ctx context.Context, _ chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	close(c.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestBackgroundCancellationDuringExecution(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "agent"}[shutdown], func(t *testing.T) {
			deps := isolatedModuleDeps(t)
			client := backgroundBlockingClient{entered: make(chan struct{})}
			models, err := modelmgr.New(modelmgr.Options{
				Clients: map[string]llm.Client{"p": client}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}},
				ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work",
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = models.Close(context.Background()) })
			deps.Models = models
			m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			ctx := workspace.WithPathScope(t.Context(), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			results := make(chan BackgroundResult, 1)
			errors := make(chan error, 1)
			go func() {
				result, err := m.RunBackground(ctx, BackgroundRequest{Kind: "cron", Name: "cancel", Prompt: "run"})
				results <- result
				errors <- err
			}()
			select {
			case <-client.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("model call never began")
			}
			if shutdown {
				m.BeginClose()
			} else {
				cancel()
			}
			select {
			case result := <-results:
				if result.SessionID == "" || result.Outcome != "cancelled" {
					t.Fatal(result)
				}
				if err := <-errors; err == nil {
					t.Fatal("cancellation returned success")
				}
				if requests := m.requests.ListBySession(result.SessionID); len(requests) != 0 {
					t.Fatalf("requests remained: %+v", requests)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("model call ignored cancellation")
			}
		})
	}
}

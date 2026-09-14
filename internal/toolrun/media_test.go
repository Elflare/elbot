package toolrun

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
)

type mediaArgumentRecorder struct{ received string }

func (t *mediaArgumentRecorder) Name() string { return "record_media" }
func (t *mediaArgumentRecorder) Info() tool.Info {
	return tool.Info{Name: t.Name(), Risk: tool.RiskLow}
}
func (t *mediaArgumentRecorder) Schema() llm.ToolSchema {
	return tool.NewBuilder(t.Name()).BuildSchema()
}
func (t *mediaArgumentRecorder) Call(_ context.Context, req tool.CallRequest) (*tool.Result, error) {
	t.received = string(req.Arguments)
	return &tool.Result{Content: "ok"}, nil
}

type failingMediaTool struct{}

func (failingMediaTool) Name() string    { return "failing_media" }
func (failingMediaTool) Info() tool.Info { return tool.Info{Name: "failing_media", Risk: tool.RiskLow} }
func (failingMediaTool) Schema() llm.ToolSchema {
	return tool.NewBuilder("failing_media").BuildSchema()
}
func (failingMediaTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	return nil, errors.New("tool failed")
}

func TestRunRetainsExactMediaArgumentsForSession(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	center := media.NewManager(store, t.TempDir(), &media.LocalBackend{Root: t.TempDir()})
	item, err := center.ImportBytes(ctx, []byte("tool input"), media.Input{Name: "input.bin"})
	if err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, tl tool.Tool, session *storage.Session, arguments string) RunResult {
		t.Helper()
		registry := tool.NewRegistry()
		if err := registry.Register(tl); err != nil {
			t.Fatal(err)
		}
		manager := NewManager(registry, nil)
		manager.Media = center
		return manager.Run(ctx, &runnerTestDeps{}, RunRequest{
			Session: session, Actor: security.Actor{Role: security.RoleSuperadmin},
			Calls: []llm.ToolCallRequest{{ID: "call-1", Name: tl.Name(), Arguments: arguments}},
		})
	}

	session := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s", Mode: storage.SessionModeWork}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	recorder := &mediaArgumentRecorder{}
	arguments := `{"nested":{"media":"` + item.ID + `"},"text":"prefix ` + item.ID + `"}`
	run(t, recorder, session, arguments)
	if recorder.received != arguments {
		t.Fatalf("tool arguments = %q", recorder.received)
	}
	refs, err := store.MediaReferences().ListByOwner(ctx, "session_tool", session.ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != item.ID {
		t.Fatalf("session tool references = %#v, %v", refs, err)
	}

	failedSession := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s", Mode: storage.SessionModeWork}
	if err := store.Sessions().Create(ctx, failedSession); err != nil {
		t.Fatal(err)
	}
	result := run(t, failingMediaTool{}, failedSession, `{"media":"`+item.ID+`"}`)
	if len(result.Messages) != 1 || !strings.Contains(llm.SegmentsContentText(result.Messages[0].Segments), "tool failed") {
		t.Fatalf("failed tool result = %#v", result.Messages)
	}
	if refs, err := store.MediaReferences().ListByOwner(ctx, "session_tool", failedSession.ID); err != nil || len(refs) != 1 {
		t.Fatalf("failed tool references = %#v, %v", refs, err)
	}
}

func TestRunDoesNotRetainMediaBeforeExecution(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	center := media.NewManager(store, t.TempDir(), &media.LocalBackend{Root: t.TempDir()})
	item, err := center.ImportBytes(ctx, []byte("tool input"), media.Input{})
	if err != nil {
		t.Fatal(err)
	}
	session := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s", Mode: storage.SessionModeWork}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	if err := registry.Register(runnerPreflightTool{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, nil)
	manager.Media = center
	manager.Run(ctx, &runnerTestDeps{}, RunRequest{
		Session: session, Actor: security.Actor{Role: security.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{ID: "call-1", Name: "preflight_tool", Arguments: `{"media":"` + item.ID + `"}`}},
	})
	if refs, err := store.MediaReferences().ListByOwner(ctx, "session_tool", session.ID); err != nil || len(refs) != 0 {
		t.Fatalf("preflight references = %#v, %v", refs, err)
	}

	missingSession := &storage.Session{OwnerID: "u", Platform: "test", PlatformScopeID: "s", Mode: storage.SessionModeWork}
	if err := store.Sessions().Create(ctx, missingSession); err != nil {
		t.Fatal(err)
	}
	recorder := &mediaArgumentRecorder{}
	registry = tool.NewRegistry()
	if err := registry.Register(recorder); err != nil {
		t.Fatal(err)
	}
	manager = NewManager(registry, nil)
	manager.Media = center
	missingID := media.IDPrefix + strings.Repeat("f", 64)
	result := manager.Run(ctx, &runnerTestDeps{}, RunRequest{
		Session: missingSession, Actor: security.Actor{Role: security.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{ID: "call-2", Name: recorder.Name(), Arguments: `{"media":"` + missingID + `"}`}},
	})
	if recorder.received != "" || len(result.Messages) != 1 || !strings.Contains(llm.SegmentsContentText(result.Messages[0].Segments), "retain media arguments") {
		t.Fatalf("missing media execution = %q, %#v", recorder.received, result.Messages)
	}
	if refs, err := store.MediaReferences().ListByOwner(ctx, "session_tool", missingSession.ID); err != nil || len(refs) != 0 {
		t.Fatalf("missing media references = %#v, %v", refs, err)
	}
}

func TestMediaArgumentsRemainExplicit(t *testing.T) {
	target := &mediaArgumentRecorder{}
	registry := tool.NewRegistry()
	if err := registry.Register(target); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, nil)
	arguments := `{ "source":"media:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "payload":{"text":"unchanged"} }`
	if !json.Valid([]byte(arguments)) {
		t.Fatal("bad fixture")
	}
	call := llm.ToolCallRequest{Name: target.Name(), Arguments: arguments}
	resolved := ResolvedTool{Name: target.Name(), Available: true, Native: target}
	if _, err := manager.AssessRisk(context.Background(), resolved, arguments); err != nil {
		t.Fatal(err)
	}
	result := manager.Execute(context.Background(), call, resolved, security.Actor{Role: security.RoleSuperadmin})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if target.received != arguments || result.Call.Arguments != arguments {
		t.Fatalf("rewritten call: %#v, %s", result.Call, target.received)
	}
}

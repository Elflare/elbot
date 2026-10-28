package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/security"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	toolregistry "elbot/internal/tool/registry"
)

type mediaArgumentRecorder struct{ received string }

func (t *mediaArgumentRecorder) Name() string { return "record_media" }
func (t *mediaArgumentRecorder) Info() toolshare.Info {
	return toolshare.Info{Name: t.Name(), Risk: toolshare.RiskLow}
}
func (t *mediaArgumentRecorder) Schema() llm.ToolSchema {
	return toolshare.NewBuilder(t.Name()).BuildSchema()
}
func (t *mediaArgumentRecorder) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		t.received = string(req.Arguments)
		{
			result := &toolshare.Result{Content: "ok"}
			return result, result.Content, nil
		}
	}), nil
}

type failingMediaTool struct{}

func (failingMediaTool) Name() string { return "failing_media" }
func (failingMediaTool) Info() toolshare.Info {
	return toolshare.Info{Name: "failing_media", Risk: toolshare.RiskLow}
}
func (failingMediaTool) Schema() llm.ToolSchema {
	return toolshare.NewBuilder("failing_media").BuildSchema()
}
func (value failingMediaTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		{
			callErr := errors.New("tool failed")
			return nil, callErr.Error(), callErr
		}
	}), nil
}

func TestRunValidatesExactMediaArguments(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	center := mediatest.New(t, store, t.TempDir())
	item, err := center.ImportBytes(ctx, []byte("tool input"), media.Input{Name: "input.bin"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, tl toolshare.Tool, arguments string) toolshare.RunResult {
		t.Helper()
		registry := toolregistry.NewRegistry(security.DefaultPermission())
		if err := registry.Register(tl); err != nil {
			t.Fatal(err)
		}
		manager := newTestManager(registry, nil, center)
		return manager.Run(ctx, &runnerTestDeps{}, toolshare.RunRequest{Committer: &testToolCommitter{},
			Session: &storage.Session{ID: "session", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
			Calls: []llm.ToolCallRequest{{ID: "call-1", Name: tl.Name(), Arguments: arguments}},
		})
	}

	recorder := &mediaArgumentRecorder{}
	arguments := `{"nested":{"media":"` + item.ID + `"},"text":"prefix ` + item.ID + `"}`
	run(t, recorder, arguments)
	if recorder.received != arguments {
		t.Fatalf("tool arguments = %q", recorder.received)
	}

	result := run(t, failingMediaTool{}, `{"media":"`+item.ID+`"}`)
	if len(result.Messages) != 1 || !strings.Contains(llm.SegmentsContentText(result.Messages[0].Segments), "tool failed") {
		t.Fatalf("failed tool result = %#v", result.Messages)
	}
}

func TestRunRejectsMissingMediaBeforeExecution(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	center := mediatest.New(t, store, t.TempDir())
	missingSession := &storage.Session{ID: "missing-session", Mode: storage.SessionModeWork}
	recorder := &mediaArgumentRecorder{}
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(recorder); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, nil, center)
	missingID := media.IDPrefix + strings.Repeat("f", 64)
	result := manager.Run(ctx, &runnerTestDeps{}, toolshare.RunRequest{Committer: &testToolCommitter{},
		Session: missingSession, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{ID: "call-2", Name: recorder.Name(), Arguments: `{"media":"` + missingID + `"}`}},
	})
	if recorder.received != "" || len(result.Messages) != 1 || !strings.Contains(llm.SegmentsContentText(result.Messages[0].Segments), "retain media arguments") {
		t.Fatalf("missing media execution = %q, %#v", recorder.received, result.Messages)
	}
}

func TestMediaArgumentsRemainExplicit(t *testing.T) {
	target := &mediaArgumentRecorder{}
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(target); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, nil)
	arguments := `{ "source":"media:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "payload":{"text":"unchanged"} }`
	if !json.Valid([]byte(arguments)) {
		t.Fatal("bad fixture")
	}
	call := llm.ToolCallRequest{Name: target.Name(), Arguments: arguments}
	resolved := resolvedTool{ResultStatus: events.ResultSucceeded, Native: target}
	prepared, err := manager.prepareCall(context.Background(), resolved, call)
	if err != nil {
		t.Fatal(err)
	}
	result := manager.executePrepared(context.Background(), call, resolved, prepared, nil)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if target.received != arguments || result.Call.Arguments != arguments {
		t.Fatalf("rewritten call: %#v, %s", result.Call, target.received)
	}
}

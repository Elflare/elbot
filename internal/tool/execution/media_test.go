package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
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
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, t.TempDir())
	item, err := center.ImportBytes(ctx, []byte("tool input"), media.Input{Name: "input.bin"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, tl toolshare.ToolDefinition, arguments string) *testToolCommitter {
		t.Helper()
		registry := toolregistry.NewRegistry(security.DefaultPermission())
		if err := registerTestTools(registry, tl); err != nil {
			t.Fatal(err)
		}
		manager := newTestManager(registry, nil, center)
		committer := &testToolCommitter{}
		result := manager.Run(ctx, &runnerTestDeps{}, toolshare.RunRequest{Committer: committer,
			Session: &storage.Session{ID: "session", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
			Calls: []llm.ToolCallRequest{{ID: "call-1", Name: tl.Definition.Name, Arguments: arguments}},
		})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		return committer
	}

	recorder := &mediaArgumentRecorder{}
	arguments := `{"nested":{"media":"` + item.ID + `"},"text":"prefix ` + item.ID + `"}`
	run(t, recorder.Definition(), arguments)
	if recorder.received != arguments {
		t.Fatalf("tool arguments = %q", recorder.received)
	}

	committer := run(t, (failingMediaTool{}).Definition(), `{"media":"`+item.ID+`"}`)
	if len(committer.messages) != 1 || !strings.Contains(llm.SegmentsContentText(committer.messages[0].Segments), "tool failed") {
		t.Fatalf("failed tool result = %#v", committer.messages)
	}
}

func TestRunRejectsMissingMediaBeforeExecution(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, t.TempDir())
	missingSession := &storage.Session{ID: "missing-session", Mode: storage.SessionModeWork}
	recorder := &mediaArgumentRecorder{}
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, recorder.Definition()); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, nil, center)
	missingID := media.IDPrefix + strings.Repeat("f", 64)
	committer := &testToolCommitter{}
	result := manager.Run(ctx, &runnerTestDeps{}, toolshare.RunRequest{Committer: committer,
		Session: missingSession, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{ID: "call-2", Name: recorder.Name(), Arguments: `{"media":"` + missingID + `"}`}},
	})
	if result.Err != nil || recorder.received != "" || len(committer.messages) != 1 || !strings.Contains(llm.SegmentsContentText(committer.messages[0].Segments), "retain media arguments") {
		t.Fatalf("missing media execution = %q, result = %+v, messages = %#v", recorder.received, result, committer.messages)
	}
}

func TestMediaArgumentsRemainExplicit(t *testing.T) {
	target := &mediaArgumentRecorder{}
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, target.Definition()); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, nil)
	arguments := `{ "source":"media:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "payload":{"text":"unchanged"} }`
	if !json.Valid([]byte(arguments)) {
		t.Fatal("bad fixture")
	}
	call := llm.ToolCallRequest{Name: target.Name(), Arguments: arguments}
	definition := target.Definition()
	resolved := resolvedTool{Native: &definition}
	prepared, err := manager.prepareCall(context.Background(), resolved, call)
	if err != nil {
		t.Fatal(err)
	}
	result := manager.executePrepared(context.Background(), call, resolved, prepared, nil)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if target.received != arguments {
		t.Fatalf("rewritten arguments: %s", target.received)
	}
}

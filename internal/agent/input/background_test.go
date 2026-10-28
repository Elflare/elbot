package input

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type backgroundHarness struct {
	permission                        *security.Permission
	scope                             session.Scope
	prepareErr, preloadErr, commitErr error
	preloads, commits, executions     int
	preload                           tool.BackgroundPreload
	input                             turn.Input
	runContext                        context.Context
	result                            turn.Result
	ensure                            func(context.Context, string, string) error
}

func (h *backgroundHarness) PlatformName() string { return "cli" }
func (h *backgroundHarness) PrepareBackground(ctx context.Context, scope session.Scope, req session.BackgroundRequest) (*storage.Session, error) {
	h.scope = scope
	if h.prepareErr != nil {
		return nil, h.prepareErr
	}
	return &storage.Session{ID: "session", Metadata: "{}"}, nil
}

type backgroundPreloaderStub struct{ h *backgroundHarness }

func (p backgroundPreloaderStub) PrepareBackground(ctx context.Context, req tool.BackgroundPreloadRequest) (tool.BackgroundPreload, error) {
	p.h.preloads++
	return p.h.preload, p.h.preloadErr
}
func (h *backgroundHarness) Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, events.LogResult, error) {
	h.commits++
	return tool.StateCommit{Injected: []string{"tool"}, Metadata: "{}"}, events.LogResult{}, h.commitErr
}
func (h *backgroundHarness) ModelContext(ctx context.Context, row *storage.Session) (context.Context, error) {
	return ctx, nil
}
func (h *backgroundHarness) RunBackground(ctx context.Context, row *storage.Session, text string) turn.Result {
	h.executions++
	h.input = agentshare.InboundTurnInput(ctx, text)
	h.runContext = ctx
	return h.result
}
func newBackgroundHarness(t *testing.T) (*backgroundHarness, *BackgroundHandler, context.Context) {
	t.Helper()
	h := &backgroundHarness{permission: security.DefaultPermission(), result: turn.Result{SessionID: "session", RunID: "run", Text: "answer", Outcome: "completed"}}
	h.ensure = func(context.Context, string, string) error { return nil }
	handler := NewBackgroundHandler(BackgroundHandlerDeps{
		Sessions: h, Identity: h, Execution: h, Preloader: backgroundPreloaderStub{h}, ToolState: h,
		WithPermission:     func(ctx context.Context) context.Context { return security.WithPermission(ctx, h.permission) },
		EnsureWorkspaceDir: func(ctx context.Context, id, dir string) error { return h.ensure(ctx, id, dir) },
	})
	ctx := workspace.WithPathScope(t.Context(), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
	ctx = contextinfo.WithBackgroundKind(ctx, contextinfo.BackgroundKindCron)
	return h, handler, ctx
}

func TestBackgroundPreparationKeepsContextAndMultimodalSkillMaterial(t *testing.T) {
	h, handler, ctx := newBackgroundHarness(t)
	h.preload = tool.BackgroundPreload{SkillPrompt: "SKILL_GUIDANCE", Skills: []string{"notes"}, NeedsElyphRules: true}
	req := BackgroundRequest{
		Kind: "cron", Name: "task", Prompt: "before after", ModelProvider: "provider", Model: "model",
		Actor: contextinfo.Actor{ID: "owner", Platform: "qq", Role: contextinfo.RoleUser},
		PromptSegments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "before "},
			{Type: llm.SegmentImage, MediaID: "image", MIMEType: "image/png"},
			{Type: llm.SegmentText, Text: "after"},
		},
	}
	result := handler.RunBackground(ctx, req)
	if !reflect.DeepEqual(result, h.result) {
		t.Fatal(result)
	}
	if h.scope.Platform != "qq" || h.scope.ActorID != "owner" || h.scope.PlatformScopeID != "cron:task" {
		t.Fatal(h.scope)
	}
	actor, _ := contextinfo.ActorFromContext(h.runContext)
	permission, _ := security.PermissionFromContext(h.runContext)
	models, err := modelmgr.NewModelManager(t.Context(), &config.Config{Providers: map[string]config.ProviderConfig{"provider": {}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "provider", Model: "default"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := models.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	selected := modelmgr.SelectionForTurn(h.runContext, models, nil).ModelSelection
	if actor != req.Actor || permission != h.permission || selected != (config.ModelSelection{Provider: "provider", Model: "model"}) {
		t.Fatalf("actor=%+v permission=%p selection=%+v", actor, permission, selected)
	}
	beforePath, _ := workspace.PathScopeFromContext(ctx)
	afterPath, _ := workspace.PathScopeFromContext(h.runContext)
	if beforePath != afterPath {
		t.Fatal(afterPath)
	}
	if !contextinfo.BackgroundContext(h.runContext) {
		t.Fatal("background execution lost its output suppression identity")
	}
	if !h.input.NeedsElyphRules || !strings.Contains(h.input.Text, "SKILL_GUIDANCE") {
		t.Fatalf("lost instructions: %+v", h.input)
	}
	imageIndex := -1
	for i, segment := range h.input.Segments {
		if segment.Type == llm.SegmentImage {
			imageIndex = i
			if segment.MediaID != "image" {
				t.Fatal(segment)
			}
		}
	}
	if imageIndex <= 0 || imageIndex == len(h.input.Segments)-1 || !strings.Contains(h.input.Segments[imageIndex-1].Text, "before") || h.input.Segments[imageIndex+1].Text != "after" {
		t.Fatalf("media order changed: %+v", h.input.Segments)
	}
}

func TestBackgroundPreparationStopsBeforeExecutionOnFailure(t *testing.T) {
	for _, stage := range []string{"session", "workspace", "preload", "commit", "taken_over"} {
		t.Run(stage, func(t *testing.T) {
			h, handler, ctx := newBackgroundHarness(t)
			failure := errors.New(stage)
			h.preload.Update = tool.StateUpdate{Tags: []string{"tag"}}
			req := BackgroundRequest{Kind: "cron", Prompt: "task"}
			switch stage {
			case "session":
				h.prepareErr = failure
			case "workspace":
				h.ensure = func(context.Context, string, string) error { return failure }
			case "preload":
				h.preloadErr = failure
			case "commit":
				h.commitErr = failure
			case "taken_over":
				h.prepareErr = session.ErrForegroundSession
				req.SessionID = "existing"
			}
			result := handler.RunBackground(ctx, req)
			if h.executions != 0 {
				t.Fatal("executed after preparation failure")
			}
			if stage == "taken_over" {
				if result.Err != nil || !result.TakenOver || result.SessionID != "existing" || result.Outcome != "taken_over" {
					t.Fatal(result)
				}
			} else if !errors.Is(result.Err, failure) {
				t.Fatal(result)
			}
			if stage != "session" && stage != "taken_over" && result.SessionID != "session" {
				t.Fatal("lost prepared session", result)
			}
		})
	}
}

func TestBackgroundContinuationDoesNotPreload(t *testing.T) {
	h, handler, ctx := newBackgroundHarness(t)
	result := handler.RunBackground(ctx, BackgroundRequest{Kind: "cron", SessionID: "session", ToolListNames: []string{"ignored"}, Prompt: "continue"})
	if result.Err != nil || h.preloads != 0 || h.commits != 0 || h.executions != 1 {
		t.Fatalf("result=%+v preload=%d commit=%d run=%d", result, h.preloads, h.commits, h.executions)
	}
}

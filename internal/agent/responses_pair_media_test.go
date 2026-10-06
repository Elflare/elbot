package agent

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/tool"
)

func TestResponsesToolForkResolvesResultMediaForContinuationAndReplay(t *testing.T) {
	var center *media.Manager
	var imageID string
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "image", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		return &tool.Result{Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "saved tool image"}, {Type: llm.SegmentImage, MediaID: imageID, Name: "tiny.png"}}}, nil
	}})
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		switch index {
		case 0:
			emitNative(w, "image-head", "completed", nativeCall("image-call", "image", `{}`))
		case 1:
			emitNative(w, "source-final", "completed", nativeText("source final"))
		case 2, 3:
			body := inputJSON(request)
			if !strings.Contains(body, "data:image/png;base64,") || strings.Count(body, `"text":"saved tool image"`) != 1 {
				t.Errorf("branch result media was not resolved exactly once: %s", body)
			}
			if index == 2 {
				if request.PreviousResponseID != "image-head" {
					t.Errorf("continuation=%+v", request)
				}
				nativeChainError(w)
			} else {
				if request.PreviousResponseID != "" {
					t.Errorf("replay=%+v", request)
				}
				emitNative(w, "branch-final", "completed", nativeText("branch final"))
			}
		default:
			t.Errorf("unexpected request=%d", index)
		}
	}, func(opts *testAgentOptions) {
		root := filepath.Join(t.TempDir(), "media")
		center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
		opts.Media, opts.ToolRegistry = center, registry
	})
	item, err := center.ImportBytes(t.Context(), []byte("tiny image"), media.Input{Name: "tiny.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	imageID = item.ID
	if err := f.agent.HandleMessage(t.Context(), "@tool:image run"); err != nil {
		t.Fatal(err)
	}
	source := fixtureSession(t, f)
	rows, err := f.store.Messages().ListBySession(t.Context(), source.ID)
	if err != nil || len(rows) != 4 {
		t.Fatalf("source=%+v %v", rows, err)
	}
	branch, err := f.agent.execution.sessions.Fork(t.Context(), f.agent.Scope(t.Context()), rows[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := f.store.Dialogues().Seed(t.Context(), branch.ID)
	if err != nil || !strings.Contains(seed.MaterialsJSON, imageID) {
		t.Fatalf("seed=%+v %v", seed, err)
	}
	if err := f.store.Sessions().Delete(t.Context(), source.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "describe the saved tool image"); err != nil {
		t.Fatal(err)
	}
	if len(f.captured()) != 4 {
		t.Fatalf("requests=%d", len(f.captured()))
	}
}

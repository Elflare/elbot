package agent

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/media"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
)

func TestResponsesToolForkResolvesResultMediaForContinuationAndReplay(t *testing.T) {
	for _, mode := range []string{"stored", "stateless"} {
		t.Run(mode, func(t *testing.T) {
			stateless := mode == "stateless"
			var center *media.Manager
			var imageID string
			registry := tool.NewRegistry()
			f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
				if index > 0 {
					body := inputJSON(request)
					if !strings.Contains(body, `"type":"function_call_output"`) || strings.Count(body, `"type":"input_image"`) != 1 || !strings.Contains(body, "data:image/png;base64,") {
						t.Errorf("view_image result missing or duplicated: %s", body)
					}
				}
				switch index {
				case 0:
					emitNativeStore(w, "image-head", !stateless, nativeCall("image-call", "view_image", fmt.Sprintf(`{"source":%q}`, imageID)))
				case 1:
					emitNativeStore(w, "source-final", !stateless, nativeText("source final"))
				case 2, 3:
					if index == 2 && !stateless {
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
				if err := registry.Register(builtin.NewViewImageTool(center, nil)); err != nil {
					t.Fatal(err)
				}
				opts.Media, opts.ToolRegistry = center, registry
			})
			var data bytes.Buffer
			if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			item, err := center.ImportBytes(t.Context(), data.Bytes(), media.Input{Name: "tiny.png", MIMEType: "image/png"})
			if err != nil {
				t.Fatal(err)
			}
			imageID = item.ID
			if err := f.agent.HandleMessage(t.Context(), "@tool:view_image run"); err != nil {
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
			if stateless && seed.ResponseID != "" {
				t.Fatalf("stateless fork retained response ID: %+v", seed)
			}
			if err := f.store.Sessions().Delete(t.Context(), source.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.agent.HandleMessage(t.Context(), "describe the saved tool image"); err != nil {
				t.Fatal(err)
			}
			want := 4
			if stateless {
				want = 3
			}
			if len(f.captured()) != want {
				t.Fatalf("requests=%d", len(f.captured()))
			}
		})
	}
}

package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	toolmedia "elbot/internal/tool/media"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/media"
	"elbot/internal/processenv"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestSkillMediaBridge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("input"), media.Input{Name: "in.txt"})
	if err != nil {
		t.Fatal(err)
	}
	mediaRuntime := toolmedia.NewMediaRuntime(center, t.TempDir())
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := processenv.Environment{}.Overlay(map[string]string{"ELBOT_SKILL_TEST_HELPER": "media"})
	catalog := newCatalog()
	catalog.Replace([]record{{Name: "media", Kind: KindGo, Root: dir, BinaryPath: binary}})
	runner := NewGoRunner(&Manager{catalog: catalog, processEnv: environment, media: mediaRuntime})
	manifest := AgentSkillManifest{Command: []string{binary}, Args: map[string]string{"input": "--input", "inputs": "--inputs", "mode": "--mode"}, Parameters: map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "media"}, "inputs": map[string]any{"type": "array", "items": map[string]any{"type": "media"}, "maxItems": 10}, "mode": map[string]any{"type": "string"}}}}
	command := newCommandTool(record{Name: "command", Kind: KindAgent, Root: dir, Manifest: manifest}, mediaRuntime, environment)
	schema := manifest.Schema("command", "")
	props := schema.Parameters["properties"].(map[string]any)
	projectedInput := props["input"].(map[string]any)
	projectedItems := props["inputs"].(map[string]any)["items"].(map[string]any)
	manifestItems := manifest.Parameters["properties"].(map[string]any)["inputs"].(map[string]any)["items"].(map[string]any)
	if projectedInput["type"] != "string" || projectedItems["type"] != "string" || projectedItems["pattern"] == "" || manifestItems["type"] != "media" {
		t.Fatal("invalid schema projection or mutated manifest")
	}
	assertPrepared := func(t *testing.T) {
		t.Helper()
		refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
		if err != nil || len(refs) != 1 || refs[0].OwnerType != "skill" {
			t.Fatalf("input was not retained before execution: %v %v", refs, err)
		}
		matches, err := filepath.Glob(filepath.Join(dir, ".media-call-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("media workspace was not prepared: %v %v", matches, err)
		}
	}
	assertCleanup := func(t *testing.T) {
		t.Helper()
		refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
		if err != nil || len(refs) != 0 {
			t.Fatalf("refs %v %v", refs, err)
		}
		matches, err := filepath.Glob(filepath.Join(dir, ".media-call-*"))
		if err != nil || len(matches) != 0 {
			t.Fatalf("temporary files survived: %v %v", matches, err)
		}
	}
	for _, kind := range []string{"go", "toml"} {
		for _, mode := range []string{"ok", "large", "fail", "escape", "wait", "cancel"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				run := func(t *testing.T) {
					runCtx := ctx
					if mode == "cancel" {
						canceled, cancel := context.WithCancel(ctx)
						cancel()
						runCtx = canceled
					}

					execute := runMediaCommand
					if mode == "wait" {
						execute = func(ctx context.Context, label string, cmd *exec.Cmd, mediaCall toolshare.MediaCall) (*toolshare.Result, error) {
							want := 100 * time.Millisecond
							if kind == "toml" {
								want = time.Second
							}
							deadline, ok := ctx.Deadline()
							if !ok || deadline.Sub(time.Now()) != want || ctx.Err() != nil {
								t.Fatalf("wrong deadline: %v %v %v", deadline, ok, ctx.Err())
							}
							assertPrepared(t)
							<-ctx.Done()
							return nil, commandError(ctx, label, ctx.Err(), "", "")
						}
					}
					var raw []byte
					var result *toolshare.Result
					var err error
					if kind == "go" {
						timeout := 0
						if mode == "wait" {
							timeout = 100
						}
						raw, _ = json.Marshal(map[string]any{"skill_name": "media", "timeout_ms": timeout, "payload": map[string]any{"mode": mode, "media_inputs": []toolshare.MediaInput{{MediaID: item.ID}}}})
						args, parseErr := parseGoRunnerArgs(raw)
						if parseErr != nil {
							t.Fatal(parseErr)
						}
						record, _ := catalog.Get("media")
						result, _, err = runner.call(runCtx, args, record, execute)
					} else {
						current := command
						if mode == "wait" {
							current.manifest.TimeoutSeconds = 1
						}
						raw, _ = json.Marshal(map[string]any{"input": item.ID, "mode": mode})
						args, parseErr := commandArguments(current.manifest, raw)
						if parseErr != nil {
							t.Fatal(parseErr)
						}
						result, _, err = current.call(runCtx, raw, args, execute)
					}
					if mode == "ok" || mode == "large" {
						if err != nil || len(result.Segments) != 2 {
							t.Fatalf("result %#v %v", result, err)
						}
						segment := result.Segments[1]
						data, _, readErr := center.Read(ctx, segment.MediaID)
						if readErr != nil || string(data) != "output" || segment.URL != "" {
							t.Fatalf("result media %#v %q %v", segment, data, readErr)
						}
						refs, _ := store.MediaReferences().ListMediaIDs(ctx, segment.MediaID)
						if len(refs) != 0 {
							t.Fatal("output refs leaked")

						}
						session := &storage.Session{OwnerID: "skill-test", Platform: "test", PlatformScopeID: "test"}
						if err := store.Sessions().Create(ctx, session); err != nil {
							t.Fatal(err)
						}
						segments, _ := json.Marshal(result.LLMSegments())
						message := &storage.Message{SessionID: session.ID, Role: storage.RoleTool, Segments: string(segments)}
						if err := store.Messages().Append(ctx, message); err != nil {
							t.Fatal(err)
						}
						persistent, err := store.MediaReferences().ListByOwner(ctx, "tool_result", message.ID)
						if err != nil || len(persistent) != 1 || persistent[0].MediaID != segment.MediaID {
							t.Fatalf("transcript refs %v %v", persistent, err)
						}
						if err := store.Sessions().Delete(ctx, session.ID); err != nil {
							t.Fatal(err)
						}
					} else if err == nil {
						t.Fatalf("%s succeeded", mode)
					}

					if mode == "wait" && !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("not a deadline failure: %v", err)
					}
					if mode == "cancel" && !errors.Is(err, context.Canceled) {
						t.Fatalf("not a cancellation failure: %v", err)
					}
					assertCleanup(t)
				}
				if mode == "wait" {
					synctest.Test(t, run)
				} else {
					run(t)
				}
			})
		}
		t.Run(kind+"/cancel running", func(t *testing.T) {
			runCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := filepath.Join(t.TempDir(), "ready")
			environment := environment.Overlay(map[string]string{"ELBOT_SKILL_TEST_READY_FILE": ready})
			done := make(chan error, 1)
			exited := make(chan struct{})
			go func() {
				defer close(exited)
				if kind == "go" {
					current := NewGoRunner(&Manager{catalog: catalog, processEnv: environment, media: mediaRuntime})
					raw, _ := json.Marshal(map[string]any{"skill_name": "media", "payload": map[string]any{"mode": "wait", "media_inputs": []toolshare.MediaInput{{MediaID: item.ID}}}})
					_, _, err := callTool(runCtx, current, toolshare.CallRequest{Arguments: raw})
					done <- err
				} else {
					current := command
					current.processEnv = environment
					raw, _ := json.Marshal(map[string]any{"input": item.ID, "mode": "wait"})
					_, _, err := callTool(runCtx, current, toolshare.CallRequest{Arguments: raw})
					done <- err
				}
			}()
			waitForSkillHelperReady(t, ready, exited)
			assertPrepared(t)
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("running skill cancellation: %v", err)
			}
			assertCleanup(t)
		})
	}
	call := mediaRuntime.NewCall()
	defer call.Close()
	raw := json.RawMessage(fmt.Sprintf(`{ "text":%q }`, item.ID))
	unchanged, err := prepareGoMedia(ctx, raw, call, dir)
	if err != nil || string(unchanged) != string(raw) {
		t.Fatalf("ordinary Go payload changed: %s %v", unchanged, err)
	}
	large, err := center.ImportBytes(ctx, []byte(strings.Repeat("x", 1024*1024+1)), media.Input{Name: "large.bin"})
	if err != nil {
		t.Fatal(err)
	}
	largeRaw, _ := json.Marshal(map[string]any{"media_inputs": []toolshare.MediaInput{{MediaID: large.ID}, {MediaID: item.ID}, {MediaID: large.ID}, {MediaID: item.ID}}})
	prepared, err := prepareGoMedia(ctx, largeRaw, call, dir)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Inputs []struct {
			MediaID  string `json:"media"`
			Path     string `json:"path"`
			Name     string `json:"name"`
			MIMEType string `json:"mime_type"`
			Base64   string `json:"base64"`
			Size     int64  `json:"size"`
		} `json:"media_inputs"`
	}
	if err := json.Unmarshal(prepared, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Inputs) != 4 || payload.Inputs[0] != payload.Inputs[2] || payload.Inputs[1] != payload.Inputs[3] {
		t.Fatalf("input order or duplicate entries changed: %#v", payload)
	}
	for i, metadata := range []*storage.Media{large, item} {
		input := payload.Inputs[i]
		if input.MediaID != metadata.ID || input.Name != metadata.Name || input.MIMEType != metadata.MIMEType || input.Size != metadata.Size {
			t.Fatalf("input metadata %#v, want %#v", input, metadata)
		}
		if data, err := os.ReadFile(filepath.Join(dir, input.Path)); err != nil || int64(len(data)) != metadata.Size || (i == 1 && string(data) != "input") {
			t.Fatalf("exported input size=%d error=%v", len(data), err)
		}
	}
	if payload.Inputs[0].Base64 != "" || payload.Inputs[1].Base64 != "aW5wdXQ=" {
		t.Fatal("incorrect base64 preparation")
	}
	known, err := resultFromStdoutWithMedia(ctx, fmt.Sprintf(`{"segments":[{"type":"file","media":%q}]}`, item.ID), call, dir)
	if err != nil || known.Segments[0].MediaID != item.ID {
		t.Fatalf("known media %#v %v", known, err)
	}
	explicitRaw, _ := json.Marshal(map[string]any{"input": item.ID, "inputs": []string{item.ID, large.ID, item.ID}, "mode": item.ID})
	explicit, err := prepareCommandMedia(ctx, explicitRaw, manifest, call, dir)
	if err != nil {
		t.Fatal(err)
	}
	var explicitArgs struct {
		Input  string   `json:"input"`
		Inputs []string `json:"inputs"`
		Mode   string   `json:"mode"`
	}
	if err := json.Unmarshal(explicit, &explicitArgs); err != nil {
		t.Fatal(err)
	}
	if explicitArgs.Mode != item.ID || explicitArgs.Input == item.ID || len(explicitArgs.Inputs) != 3 || explicitArgs.Inputs[0] != explicitArgs.Inputs[2] || explicitArgs.Inputs[1] == large.ID {
		t.Fatalf("implicit conversion %s", explicit)
	}
	for _, value := range []string{`null`, `42`, `"bad"`, `"media:` + strings.Repeat("f", 64) + `"`} {
		raw := json.RawMessage(`{"input":` + value + `}`)
		if _, err := prepareCommandMedia(ctx, raw, manifest, call, dir); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	for _, value := range []string{`null`, `42`, `[42]`, `["bad"]`} {
		raw := json.RawMessage(`{"inputs":` + value + `}`)
		if _, err := prepareCommandMedia(ctx, raw, manifest, call, dir); err == nil {
			t.Fatalf("accepted media array %s", value)
		}
	}
	for _, out := range []string{`{"segments":[{"type":"file"}]}`, `{"segments":[{"type":"file","path":"../escape"}]}`, `{"segments":[{"type":"file","path":"x","media":"bad"}]}`} {
		if _, err := resultFromStdoutWithMedia(ctx, out, call, dir); err == nil {
			t.Fatalf("accepted %s", out)
		}
	}
}

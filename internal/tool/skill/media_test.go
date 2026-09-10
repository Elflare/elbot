package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
)

func TestSkillMediaBridge(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
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
	mediaRuntime := &tool.MediaRuntime{Center: center, SandboxRoot: t.TempDir()}
	dir := t.TempDir()
	source := `package main
import("encoding/json";"os";"path/filepath";"time";"fmt")
func main(){
 var path,mode,workspace string
 if len(os.Args)>1 { for i:=1;i+1<len(os.Args);i+=2 { if os.Args[i]=="--input" { path=os.Args[i+1] }; if os.Args[i]=="--mode" { mode=os.Args[i+1] } }; workspace=filepath.Dir(path)
 } else { var payload struct { MediaInputs []struct{Path,Base64 string}; MediaWorkspace string; Mode string }; var raw map[string]json.RawMessage; json.NewDecoder(os.Stdin).Decode(&raw); json.Unmarshal(raw["media_inputs"],&payload.MediaInputs); json.Unmarshal(raw["media_workspace"],&workspace); json.Unmarshal(raw["mode"],&mode); if len(payload.MediaInputs)==0 || payload.MediaInputs[0].Base64!="aW5wdXQ=" { os.Exit(4) }; path=payload.MediaInputs[0].Path }
 if filepath.IsAbs(path) { os.Exit(5) }; data,err:=os.ReadFile(path); if err!=nil || string(data)!="input" { os.Exit(6) }
 if mode=="fail" { os.Exit(7) }; if mode=="wait" { time.Sleep(10*time.Second) }
 output:=filepath.Join(workspace,"output.txt"); os.WriteFile(output,[]byte("output"),0600)
 if mode=="escape" { output="../escape.txt" }
 fmt.Printf("{\"content\":\"done\",\"segments\":[{\"type\":\"file\",\"path\":%q}]}",filepath.ToSlash(output))
}`
	sourcePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "helper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, sourcePath).CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	catalog := NewCatalog()
	catalog.Replace([]Record{{Name: "media", Kind: KindGo, Root: dir, BinaryPath: binary}})
	runner := NewGoRunner(catalog)
	runner.Media = mediaRuntime
	manifest := AgentSkillManifest{Command: []string{binary}, Args: map[string]string{"input": "--input", "inputs": "--inputs", "mode": "--mode"}, Parameters: map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "media"}, "inputs": map[string]any{"type": "array", "items": map[string]any{"type": "media"}, "maxItems": 10}, "mode": map[string]any{"type": "string"}}}}
	command := NewCommandTool(Record{Name: "command", Kind: KindAgent, Root: dir, Manifest: manifest})
	command.Media = mediaRuntime
	schema := manifest.Schema("command", "")
	props := schema.Function.Parameters["properties"].(map[string]any)
	projectedInput := props["input"].(map[string]any)
	projectedItems := props["inputs"].(map[string]any)["items"].(map[string]any)
	manifestItems := manifest.Parameters["properties"].(map[string]any)["inputs"].(map[string]any)["items"].(map[string]any)
	if projectedInput["type"] != "string" || projectedItems["type"] != "string" || projectedItems["pattern"] == "" || manifestItems["type"] != "media" {
		t.Fatal("invalid schema projection or mutated manifest")
	}
	for _, kind := range []string{"go", "toml"} {
		for _, mode := range []string{"ok", "fail", "escape", "wait", "cancel"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				runCtx := ctx
				if mode == "cancel" {
					canceled, cancel := context.WithCancel(ctx)
					cancel()
					runCtx = canceled
				}
				var raw []byte
				var result *tool.Result
				var err error
				if kind == "go" {
					timeout := 0
					if mode == "wait" {
						timeout = 100
					}
					raw, _ = json.Marshal(map[string]any{"skill_name": "media", "timeout_ms": timeout, "payload": map[string]any{"mode": mode, "media_inputs": []tool.MediaInput{{MediaID: item.ID}}}})
					result, err = runner.Call(runCtx, tool.CallRequest{Arguments: raw})
				} else {
					current := command
					if mode == "wait" {
						current.Manifest.TimeoutSeconds = 1
					}
					raw, _ = json.Marshal(map[string]any{"input": item.ID, "mode": mode})
					result, err = current.Call(runCtx, tool.CallRequest{Arguments: raw})
				}
				if mode == "ok" {
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
				refs, refErr := store.MediaReferences().ListMediaIDs(ctx, item.ID)
				if refErr != nil || len(refs) != 0 {
					t.Fatalf("refs %v %v", refs, refErr)
				}
				matches, _ := filepath.Glob(filepath.Join(dir, ".media-call-*"))
				if len(matches) > 0 {
					t.Fatalf("temporary files survived: %v", matches)
				}
			})
		}
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
	largeRaw, _ := json.Marshal(map[string]any{"media_inputs": []tool.MediaInput{{MediaID: large.ID}}})
	prepared, err := prepareGoMedia(ctx, largeRaw, call, dir)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Inputs []struct {
			Path   string `json:"path"`
			Base64 string `json:"base64"`
			Size   int64  `json:"size"`
		} `json:"media_inputs"`
	}
	if err := json.Unmarshal(prepared, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Inputs) != 1 || payload.Inputs[0].Base64 != "" || payload.Inputs[0].Size != large.Size {
		t.Fatalf("large input %#v", payload)
	}
	if info, err := os.Stat(filepath.Join(dir, payload.Inputs[0].Path)); err != nil || info.Size() != large.Size {
		t.Fatalf("large export %v %v", info, err)
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

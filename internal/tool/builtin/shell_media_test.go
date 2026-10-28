package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	toolmedia "elbot/internal/tool/media"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/workspace"
)

func TestShellMediaInputsAndCleanup(t *testing.T) {
	ctx := workspace.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: t.TempDir()})
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := mediatest.New(t, store, root)
	item, err := center.ImportBytes(ctx, []byte("shell-media-content"), media.Input{Name: "input.txt"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	shell := NewShellTool()
	shell.Media = toolmedia.NewMediaRuntime(center, t.TempDir(), mediatest.Publisher(center))
	command := `cat "$ELBOT_MEDIA_1" "$ELBOT_MEDIA_2"`
	slow := "sleep 10"
	if isPowerShellEnv() {
		command = `Get-Content -LiteralPath $env:ELBOT_MEDIA_1,$env:ELBOT_MEDIA_2`
		slow = "Start-Sleep -Seconds 10"
	}
	for _, test := range []struct {
		name, cmd string
		timeout   int
		cancel    bool
	}{
		{name: "success", cmd: command}, {name: "failure", cmd: "exit 7"}, {name: "timeout", cmd: slow, timeout: 50}, {name: "cancel", cmd: command, cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runCtx := ctx
			if test.cancel {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				runCtx = canceled
			}
			raw, _ := json.Marshal(map[string]any{"cmd": test.cmd, "timeout_ms": test.timeout, "media_inputs": []toolshare.MediaInput{{MediaID: item.ID}, {MediaID: item.ID}}})
			original := string(raw)
			result, _, err := callTool(runCtx, shell, toolshare.CallRequest{Arguments: raw})
			if test.name == "success" && (err != nil || strings.Count(result.Content, "shell-media-content") != 2) {
				t.Fatalf("result %#v %v", result, err)
			}
			if test.name == "failure" && (err != nil || !strings.Contains(result.Content, "exit_code: 7")) {
				t.Fatalf("failure %#v %v", result, err)
			}
			if string(raw) != original {
				t.Fatal("arguments changed")
			}
			if _, err := center.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
			refs, refErr := store.MediaReferences().ListMediaIDs(ctx, item.ID)
			if refErr != nil || len(refs) != 0 {
				t.Fatalf("leaked refs %v %v", refs, refErr)
			}
		})
	}
	marker := filepath.Join(t.TempDir(), "started")
	cmd := `echo started > "` + filepath.ToSlash(marker) + `"`
	for _, id := range []string{"", "bad", "media:" + strings.Repeat("f", 64)} {
		raw, _ := json.Marshal(map[string]any{"cmd": cmd, "media_inputs": []map[string]string{{"media": item.ID}, {"media": id}}})
		if _, _, err := callTool(ctx, shell, toolshare.CallRequest{Arguments: raw}); err == nil {
			t.Fatalf("accepted %s", id)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("command ran: %v", err)
		}
		if _, err := center.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		refs, _ := store.MediaReferences().ListMediaIDs(ctx, item.ID)
		if len(refs) != 0 {
			t.Fatal("partial preparation leaked references")
		}
	}
}

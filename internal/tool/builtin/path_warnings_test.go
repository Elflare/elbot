package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/share"
)

func TestSendFileAbsoluteWarningBelongsToForeground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"source": path})
	tool := NewSendFileTool(nil)
	result, _, err := tool.Call(t.Context(), share.CallRequest{Arguments: args})
	if err != nil || len(result.Warnings) != 1 || result.Warnings[0] != share.AbsolutePathWorkspaceWarning {
		t.Fatalf("foreground=%+v err=%v", result, err)
	}
	ctx := contextinfo.WithBackgroundKind(t.Context(), contextinfo.BackgroundKindElnis)
	result, _, err = tool.Call(ctx, share.CallRequest{Arguments: args})
	if err != nil || len(result.Warnings) != 0 {
		t.Fatalf("background=%+v err=%v", result, err)
	}
}

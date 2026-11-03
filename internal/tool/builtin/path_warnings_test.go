package builtin

import (
	workspacepath "elbot/internal/workspace"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/share"
)

func TestSendFileAbsoluteWarningAndRestrictedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"source": path})
	tool := (SendFileTool{})
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	result, _, err := callTool(ctx, tool.Definition(), share.CallRequest{Arguments: args})
	if err != nil || len(result.Warnings) != 1 || result.Warnings[0] != share.AbsolutePathWorkspaceWarning {
		t.Fatalf("foreground=%+v err=%v", result, err)
	}
	ctx = workspacepath.WithPathScope(ctx, workspacepath.PathScope{Dir: filepath.Dir(path), Restricted: true})
	result, _, err = callTool(ctx, tool.Definition(), share.CallRequest{Arguments: args})
	if err == nil {
		t.Fatalf("background=%+v err=%v", result, err)
	}
}

package builtin

import (
	"elbot/internal/operationresult"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"elbot/internal/fileops"
	toolshare "elbot/internal/tool/share"
)

func TestFileLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	body := strings.Repeat("雪🔥", 700) + "BUSINESS_TAIL"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, logContent, err := callTool(t.Context(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	preview := result.Data.LogContent()
	if !strings.Contains(logContent, "mode=read lines=1-1/1") || !strings.Contains(result.Data.Content, body) || strings.Contains(preview, "BUSINESS_TAIL") || !strings.Contains(preview, "lines: 1-1/1") || !strings.Contains(preview, strings.Repeat("雪🔥", 200)) || !strings.HasSuffix(preview, "…") || !utf8.ValidString(preview) || utf8.RuneCountInString(preview) != 1024 {
		t.Fatalf("read log=%q business bytes=%d", logContent, len(result.Data.Content))
	}
	args, _ = json.Marshal(map[string]any{"path": path, "expected_revision": fileops.ContentRevision([]byte(body)), "edits": []map[string]any{{"operation": "overwrite", "new_text": body + "EDIT_TAIL"}}})
	result, logContent, err = callTool(t.Context(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil || string(written) != body+"EDIT_TAIL" || !strings.Contains(result.Data.Content, "EDIT_TAIL") {
		t.Fatalf("business edit changed: err=%v result=%+v", err, result)
	}
	preview = result.Data.LogContent()
	if !strings.Contains(logContent, "edited=") || strings.Contains(logContent, "diff:") || !strings.Contains(preview, "diff:") || !strings.Contains(preview, "revision_before") || !strings.HasSuffix(preview, "…") || !utf8.ValidString(preview) || utf8.RuneCountInString(preview) != 1024 {
		t.Fatalf("edit log=%q", logContent)
	}
}

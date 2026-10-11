package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"elbot/internal/fileops"
	"elbot/internal/tool"
)

func TestFileLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	body := strings.Repeat("雪🔥", 200) + "BUSINESS_TAIL"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, logContent, err := NewReadFileTool().Call(t.Context(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, body) || strings.Contains(logContent, "BUSINESS_TAIL") || !strings.Contains(logContent, "lines=1-1/1") || !strings.HasSuffix(logContent, "…") || !utf8.ValidString(logContent) || utf8.RuneCountInString(logContent) != 128 {
		t.Fatalf("read log=%q business bytes=%d", logContent, len(result.Content))
	}
	args, _ = json.Marshal(map[string]any{"path": path, "expected_revision": fileops.ContentRevision([]byte(body)), "edits": []map[string]any{{"operation": "overwrite", "new_text": body + "EDIT_TAIL"}}})
	result, logContent, err = NewEditFileTool().Call(t.Context(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil || string(written) != body+"EDIT_TAIL" || !strings.Contains(result.Content, "EDIT_TAIL") {
		t.Fatalf("business edit changed: err=%v result=%+v", err, result)
	}
	if !strings.Contains(logContent, "diff:") || strings.Contains(logContent, "revision_before") || !strings.HasSuffix(logContent, "…") || !utf8.ValidString(logContent) || utf8.RuneCountInString(logContent) != 256 {
		t.Fatalf("edit log=%q", logContent)
	}
}

package builtin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/tool/share"
)

func callDraft(t *testing.T, def share.ToolDefinition, session, args string) operationresult.Result[*share.Result] {
	t.Helper()
	ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{SessionID: session})
	result, _, err := callTool(ctx, def, share.CallRequest{Name: "draft", Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDraftFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	def := draft(root)
	content := func(session, args string) string {
		t.Helper()
		r := callDraft(t, def, session, args)
		if r.Status != operationresult.Success {
			t.Fatalf("%s: %s: %s", args, r.Status, r.Message)
		}
		return r.Data.Content
	}
	if got := content("A", `{}`); got != `[]` {
		t.Fatalf("empty list: %q", got)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("listing created draft directory: %v", err)
	}
	if got := content("A", `{"name":"notes.md","content":"one\n"}`); got != "one\n" {
		t.Fatalf("create returned %q", got)
	}
	path := filepath.Join(root, "A", "notes.md")
	var files [][2]string
	if err := json.Unmarshal([]byte(content("A", `{}`)), &files); err != nil || len(files) != 1 || files[0] != [2]string{"notes.md", path} {
		t.Fatalf("list = %v, %v", files, err)
	}
	r := callDraft(t, def, "A", `{"name":"notes.md","content":"two"}`)
	if r.Message == "" && (r.Data == nil || r.Data.Content == "") {
		t.Fatal("duplicate produced no notice")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "one\n" {
		t.Fatalf("duplicate changed file: %q, %v", b, err)
	}
	if err := os.WriteFile(path, []byte("from editor\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var read [2]string
	if err := json.Unmarshal([]byte(content("A", `{"name":"notes.md"}`)), &read); err != nil || read != [2]string{"from editor\n", path} {
		t.Fatalf("read = %v, %v", read, err)
	}
	if got := content("B", `{}`); got != `[]` {
		t.Fatalf("another session saw %q", got)
	}
	if got := content("B", `{"name":"notes.md","content":""}`); got != "" {
		t.Fatalf("empty create returned %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(root, "B", "notes.md")); err != nil || len(b) != 0 {
		t.Fatalf("empty draft: %q, %v", b, err)
	}
}

func TestDraftInvalidArguments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	def := draft(root)
	for _, args := range []string{
		`{"content":"orphan"}`, `{"name":""}`, `{"name":"."}`, `{"name":"/abs.md"}`,
		`{"name":"../other.md","content":"x"}`, `{"name":"..\\other.md"}`, `{"name":"C:\\other.md"}`,
	} {
		if r := callDraft(t, def, "A", args); r.Status != operationresult.InvalidArguments {
			t.Errorf("%s: got %s (%s)", args, r.Status, r.Message)
		}
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid arguments created draft directory: %v", err)
	}
}

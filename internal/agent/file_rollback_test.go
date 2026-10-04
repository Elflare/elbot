package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/utils/fileops"
)

func rollbackAgentFixture(t *testing.T) (*Agent, *fakePlatform, context.Context, *storage.Session, string) {
	t.Helper()
	opts := validConstructorOptions(t)
	p := &fakePlatform{}
	opts.Platform = p
	opts.FileRollback = tool.NewFileRollbackService(nil)
	opts.ToolRegistry = tool.NewRegistry()
	if err := builtin.RegisterAll(opts.ToolRegistry, builtin.RegisterOptions{FileRollback: opts.FileRollback}); err != nil {
		t.Fatal(err)
	}
	a, err := NewWithOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := security.WithActor(context.Background(), security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})
	row, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "files"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := (sessionWorkspaceStore{agent: a, session: row}).SetWorkspaceDir(ctx, dir); err != nil {
		t.Fatal(err)
	}
	return a, p, ctx, row, filepath.Join(dir, "file")
}

func editForRollback(t *testing.T, a *Agent, ctx context.Context, row *storage.Session, path, text string) {
	t.Helper()
	args := map[string]any{"path": path, "create": true, "edits": []map[string]any{{"operation": "overwrite", "new_text": text}}}
	absolute := path
	if !filepath.IsAbs(path) {
		dir, err := (sessionWorkspaceStore{agent: a, session: row}).GetWorkspaceDir(ctx)
		if err != nil {
			t.Fatal(err)
		}
		absolute = filepath.Join(dir, path)
	}
	if data, err := os.ReadFile(absolute); err == nil {
		args["expected_revision"] = fileops.ContentRevision(data)
	}
	raw, _ := json.Marshal(args)
	call := llm.ToolCallRequest{ID: "edit", Name: "edit_file", Arguments: string(raw)}
	ctx = agentToolRunDeps{agent: a}.PrepareToolContext(ctx, row, call)
	editor, _ := a.toolRuntime.registry.Get("edit_file")
	if _, err := editor.Call(ctx, tool.CallRequest{ID: call.ID, Arguments: raw}); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackCommandUsesSharedRecordsWithoutLLM(t *testing.T) {
	a, p, ctx, row, path := rollbackAgentFixture(t)
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	editForRollback(t, a, ctx, row, "file", "after")
	records, err := a.ListFileRollbacks(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %+v %v", records, err)
	}
	id := records[0].ID
	if err := a.HandleMessage(ctx, "/rollback"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.out.String(), path) || !strings.Contains(p.out.String(), fmt.Sprint(id)) {
		t.Fatalf("list: %s", p.out.String())
	}
	completer, _ := a.commands.Handler("rollback")
	completions := completer.(command.Completer).Complete(ctx, command.CompletionRequest{Raw: "/rollback ", Prefix: "/", Name: "rollback", Cursor: len("/rollback ")})
	if len(completions) != 1 || completions[0].Text != fmt.Sprint(id) {
		t.Fatalf("completion: %+v", completions)
	}
	for _, phase := range []string{"turn", "compact"} {
		p.out.Reset()
		if phase == "turn" {
			a.turns.StartLLM(row.ID, "running")
		} else {
			a.turns.StartCompact(row.ID)
		}
		if err := a.HandleMessage(ctx, "/rollback"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.out.String(), path) {
			t.Fatal("list blocked during active work")
		}
		if err := a.HandleMessage(ctx, fmt.Sprintf("/rollback %d", id)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.out.String(), "仍在执行") {
			t.Fatalf("busy command: %s", p.out.String())
		}
		data, _ := os.ReadFile(path)
		if string(data) != "after" {
			t.Fatal("busy command changed file")
		}
		a.turns.FinishRequest(row.ID)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, fmt.Sprintf("/rollback %d", id)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.out.String(), "已恢复编辑前内容") {
		t.Fatalf("result: %s", p.out.String())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "before" {
		t.Fatalf("restored: %q", data)
	}
	if records, err := a.ListFileRollbacks(ctx); err != nil || len(records) != 0 {
		t.Fatalf("remaining: %+v %v", records, err)
	}
	if a.modelRuntime.clients["default"] != nil {
		// The command never needs the language model; its fake would otherwise receive a request.
		if f, ok := a.modelRuntime.clients["default"].(*fakeLLM); ok && f.requestCount() != 0 {
			t.Fatal("command called LLM")
		}
	}
}

func TestRollbackCommandInvalidatesOnNewAndDeniesRegularUsers(t *testing.T) {
	a, p, ctx, row, path := rollbackAgentFixture(t)
	editForRollback(t, a, ctx, row, "file", "created")
	records, _ := a.ListFileRollbacks(ctx)
	id := records[0].ID
	userCtx := security.WithActor(ctx, security.Actor{ID: "regular", Role: security.RoleUser})
	if err := a.HandleMessage(userCtx, "/rollback"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.out.String(), "需要超级管理员权限") {
		t.Fatalf("permission: %s", p.out.String())
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.sessions.Resume(ctx, a.scope(ctx), row.ID); err != nil {
		t.Fatal(err)
	}
	if records, err := a.ListFileRollbacks(ctx); err != nil || len(records) != 0 {
		t.Fatalf("records survived switch: %+v %v", records, err)
	}
	if _, err := a.RollbackFile(ctx, id); err == nil {
		t.Fatal("old ID revived")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "created" {
		t.Fatal("switch changed file")
	}
}

func TestRollbackCommandOldNumberDoesNotTargetNewEdit(t *testing.T) {
	a, _, ctx, row, path := rollbackAgentFixture(t)
	editForRollback(t, a, ctx, row, "file", "one")
	records, _ := a.ListFileRollbacks(ctx)
	old := records[0].ID
	editForRollback(t, a, ctx, row, "file", "two")
	if _, err := a.RollbackFile(ctx, old); err == nil {
		t.Fatal("stale ID accepted")
	}
	records, _ = a.ListFileRollbacks(ctx)
	if len(records) != 1 || records[0].ID == old {
		t.Fatalf("IDs: %+v", records)
	}
	if _, err := a.RollbackFile(ctx, records[0].ID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one" {
		t.Fatalf("wrong edit restored: %q", data)
	}
}

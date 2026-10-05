package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"elbot/internal/command"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
)

func rollbackAgentFixture(t *testing.T) (*Agent, *fakePlatform, context.Context, *storage.Session, string) {
	t.Helper()
	opts := validConstructorOptions(t)
	p := &fakePlatform{}
	opts.Platform = p
	opts.FileRollback = fileops.NewService(nil)
	opts.ToolRegistry = tool.NewRegistry()
	if err := builtin.RegisterAll(opts.ToolRegistry, builtin.RegisterOptions{FileRollback: opts.FileRollback}); err != nil {
		t.Fatal(err)
	}
	a := mustNewWithOptions(t, opts)
	ctx := security.WithActor(context.Background(), security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin})
	row, err := a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "files"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := (session.NewWorkspaceStore(a.execution.sessions, a.execution.sessionRows, row.ID)).SetWorkspaceDir(ctx, dir); err != nil {
		t.Fatal(err)
	}
	return a, p, ctx, row, filepath.Join(dir, "file")
}

func editForRollback(t *testing.T, a *Agent, ctx context.Context, row *storage.Session, path, text string) {
	t.Helper()
	args := map[string]any{"path": path, "create": true, "edits": []map[string]any{{"operation": "overwrite", "new_text": text}}}
	absolute := path
	if !filepath.IsAbs(path) {
		dir, err := (session.NewWorkspaceStore(a.execution.sessions, a.execution.sessionRows, row.ID)).GetWorkspaceDir(ctx)
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
	ctx = a.execution.chat.toolDeps.PrepareToolContext(ctx, row, call)
	editor, _ := a.execution.chat.toolRuntime.registry.Get("edit_file")
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
	records, err := listTestFileRollbacks(a, ctx)
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
	completer, _ := a.message.commands.router.Handler("rollback")
	completions := completer.(command.Completer).Complete(ctx, command.CompletionRequest{Raw: "/rollback ", Prefix: "/", Name: "rollback", Cursor: len("/rollback ")})
	if len(completions) != 1 || completions[0].Text != fmt.Sprint(id) {
		t.Fatalf("completion: %+v", completions)
	}
	for _, phase := range []string{"turn", "compact"} {
		p.out.Reset()
		if phase == "turn" {
			a.execution.turns.StartLLM(row.ID, "running")
		} else {
			a.execution.turns.StartCompact(row.ID)
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
		a.execution.turns.FinishRequest(row.ID)
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
	if records, err := listTestFileRollbacks(a, ctx); err != nil || len(records) != 0 {
		t.Fatalf("remaining: %+v %v", records, err)
	}
	if a.execution.models.ClientForProvider("default") != nil {
		// The command never needs the language model; its fake would otherwise receive a request.
		if f, ok := a.execution.models.ClientForProvider("default").(*fakeLLM); ok && f.requestCount() != 0 {
			t.Fatal("command called LLM")
		}
	}
}

func TestRollbackCommandInvalidatesOnNewAndDeniesRegularUsers(t *testing.T) {
	a, p, ctx, row, path := rollbackAgentFixture(t)
	editForRollback(t, a, ctx, row, "file", "created")
	records, _ := listTestFileRollbacks(a, ctx)
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
	if _, err := a.execution.sessions.Resume(ctx, a.identity.Scope(ctx), row.ID); err != nil {
		t.Fatal(err)
	}
	if records, err := listTestFileRollbacks(a, ctx); err != nil || len(records) != 0 {
		t.Fatalf("records survived switch: %+v %v", records, err)
	}
	if _, err := rollbackTestFile(a, ctx, id); err == nil {
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
	records, _ := listTestFileRollbacks(a, ctx)
	old := records[0].ID
	editForRollback(t, a, ctx, row, "file", "two")
	if _, err := rollbackTestFile(a, ctx, old); err == nil {
		t.Fatal("stale ID accepted")
	}
	records, _ = listTestFileRollbacks(a, ctx)
	if len(records) != 1 || records[0].ID == old {
		t.Fatalf("IDs: %+v", records)
	}
	if _, err := rollbackTestFile(a, ctx, records[0].ID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one" {
		t.Fatalf("wrong edit restored: %q", data)
	}
}

func TestRollbackCommandRechecksIdleAtCommit(t *testing.T) {
	testRollbackCommandRechecksIdleAtCommit(t, false)
}

func TestPreparedFileCommandRechecksIdleAtCommit(t *testing.T) {
	testRollbackCommandRechecksIdleAtCommit(t, true)
}

func testRollbackCommandRechecksIdleAtCommit(t *testing.T, prepared bool) {
	t.Helper()
	a, _, ctx, row, path := rollbackAgentFixture(t)
	editForRollback(t, a, ctx, row, "file", "created")
	if prepared {
		var err error
		ctx, err = a.PrepareFileCommand(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	records, err := listTestFileRollbacks(a, ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	var once sync.Once
	a.execution.chat.toolRuntime.fileRollback.CheckWrite = func(string) error {
		once.Do(func() {
			locked, release, err := a.execution.sessions.EnterActivation(ctx, a.identity.Scope(ctx), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			_ = locked
			a.execution.turns.StartLLM(row.ID, "concurrent input")
			release()
		})
		return nil
	}
	if _, err := rollbackTestFile(a, ctx, records[0].ID); err == nil || !strings.Contains(err.Error(), "仍在执行") {
		t.Fatalf("rollback passed concurrent Turn: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "created" {
		t.Fatalf("file=%q err=%v", data, err)
	}
	if records, err := listTestFileRollbacks(a, ctx); err != nil || len(records) != 1 {
		t.Fatalf("backup consumed: %+v %v", records, err)
	}
	a.execution.turns.StopSession(row.ID)
}

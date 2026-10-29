package agent

import (
	"context"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type recordingBindingTools struct {
	ToolCapabilities
	bindingCtx context.Context
}

func (r *recordingBindingTools) WithBinding(ctx context.Context, b fileops.Binding, a ...fileops.CommitAdmission) context.Context {
	ctx = r.ToolCapabilities.WithBinding(ctx, b, a...)
	r.bindingCtx = context.WithoutCancel(ctx)
	return ctx
}
func TestSupervisorRollbackAndBindingInvalidation(t *testing.T) {
	deps := isolatedModuleDeps(t)
	deps.Permission.SuperadminConfirmRisk = security.RiskCritical
	files := &recordingBindingTools{ToolCapabilities: deps.Tools}
	deps.Tools = files
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{"path": path, "edits": []map[string]string{{"operation": "replace_text", "old_text": "before", "new_text": "after"}}})
	if err != nil {
		t.Fatal(err)
	}
	client := &contractChat{call: llm.ToolCallRequest{Name: "edit_file", Arguments: string(args)}}
	contractModels(t, &deps, client)
	m := contractSupervisor(t, deps)
	edit := func() uint64 {
		t.Helper()
		client.requests = nil
		if err := m.HandleMessage(t.Context(), MessageRequest{Text: "@tool:edit_file edit"}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "after" {
			t.Fatal(string(data), err)
		}
		records, err := files.ListRollbacks(files.bindingCtx)
		if err != nil || len(records) != 1 {
			t.Fatalf("records=%+v err=%v", records, err)
		}
		return records[0].ID
	}
	id := edit()
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: fmt.Sprintf("/rollback %d", id)}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "before" {
		t.Fatal(string(data), err)
	}
	oldID := edit()
	oldCtx := files.bindingCtx
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: "/new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ListRollbacks(oldCtx); err == nil {
		t.Fatal("old binding still permits rollback access")
	}
	if err := m.HandleMessage(t.Context(), MessageRequest{Text: fmt.Sprintf("/rollback %d", oldID)}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "after" {
		t.Fatal("new session used old backup", string(data), err)
	}
}
func TestSupervisorCompletionUsesPermissionsTagsAndSkills(t *testing.T) {
	cfg := isolatedAgentConfig()
	root := t.TempDir()
	cfg.ConfigPath = filepath.Join(root, "config.toml")
	cfg.Sandbox.Root = root
	cfg.ToolTags = config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"private": {Tools: []string{"read_file"}}}}
	dir := filepath.Join(root, "skills", "agent", "contract_skill")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: contract_skill\ndescription: test guidance\n---\nskill instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	deps, _ := configuredModuleResources(t, cfg)
	if err := deps.Tools.(*tool.Supervisor).Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := contractSupervisor(t, deps)
	user := contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "user", Role: contextinfo.RoleUser})
	for _, tc := range []struct {
		ctx         context.Context
		input, want string
		present     bool
	}{
		{t.Context(), "@tool:private", "@tool:private", true},
		{user, "@tool:private", "@tool:private", false},
		{t.Context(), "@tool:read_file", "@tool:read_file", true},
		{user, "@tool:read_file", "@tool:read_file", false},
		{t.Context(), "@skill:contract", "@skill:contract_skill", true},
		{user, "@skill:contract", "@skill:contract_skill", true},
		{t.Context(), "@tool:contract_skill", "@tool:contract_skill", false},
		{user, "/rollback", "/rollback", false},
	} {
		items, err := m.Complete(tc.ctx, completion.Request{Text: tc.input})
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(completion.Texts(items), tc.want) != tc.present {
			t.Errorf("%s completions=%v", tc.input, items)
		}
	}
}

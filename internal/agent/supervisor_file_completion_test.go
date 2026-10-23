package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"elbot/internal/completion"
	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	"elbot/internal/tool"
)

type contractFileTool struct {
	contractTool
	files      *fileops.Service
	path       string
	backup     uint64
	bindingCtx context.Context
}

func (v *contractFileTool) Call(ctx context.Context, _ tool.CallRequest) (*tool.Result, string, error) {
	v.bindingCtx = context.WithoutCancel(ctx)
	_, _, err := v.files.Edit(ctx, fileops.EditRequest{Path: v.path, Edits: []fileops.Edit{{Operation: "replace_text", OldText: "before", NewText: new("after")}}})
	if err != nil {
		return nil, "", err
	}
	records, err := v.files.List(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(records) != 1 {
		return nil, "", fmt.Errorf("backups=%d", len(records))
	}
	v.backup = records[0].ID
	return &tool.Result{Content: "edited"}, "", nil
}
func TestSupervisorRollbackAndBindingInvalidation(t *testing.T) {
	deps := isolatedModuleDeps(t)
	files := deps.Tools.(isolatedToolCapabilities).Service
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	editor := &contractFileTool{files: files, path: path}
	if err := deps.Tools.(isolatedToolCapabilities).Registry.Register(editor); err != nil {
		t.Fatal(err)
	}
	client := &contractChat{}
	contractModels(t, &deps, client)
	m := contractSupervisor(t, deps)
	if err := m.HandleMessage(t.Context(), "@tool:contract_echo edit"); err != nil {
		t.Fatal(err)
	}
	if editor.backup == 0 {
		t.Fatal("tool edit did not create backup")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "after" {
		t.Fatal(string(data), err)
	}
	if err := m.HandleMessage(t.Context(), fmt.Sprintf("/rollback %d", editor.backup)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "before" {
		t.Fatal(string(data), err)
	}
	client.requests = nil
	if err := m.HandleMessage(t.Context(), "@tool:contract_echo edit again"); err != nil {
		t.Fatal(err)
	}
	oldCtx, oldID := editor.bindingCtx, editor.backup
	if err := m.HandleMessage(t.Context(), "/new"); err != nil {
		t.Fatal(err)
	}
	if _, err := files.List(oldCtx); err == nil {
		t.Fatal("old binding still permits rollback access")
	}
	if err := m.HandleMessage(t.Context(), fmt.Sprintf("/rollback %d", oldID)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "after" {
		t.Fatal("new session used old backup", string(data), err)
	}
}

type contractPrivateTool struct{ contractTool }

func (*contractPrivateTool) Name() string { return "contract_private" }
func (v *contractPrivateTool) Info() tool.Info {
	return tool.Info{Name: v.Name(), Risk: tool.RiskSafe, SuperadminOnly: true, Tags: []string{"private"}}
}

type contractSkill struct{ contractTool }

func (*contractSkill) Name() string { return "contract_skill" }
func (v *contractSkill) Info() tool.Info {
	return tool.Info{Name: v.Name(), Source: tool.SourceSkillAgent, Risk: tool.RiskSafe, OwnerScoped: true}
}
func (*contractSkill) Detail() string          { return "skill instructions" }
func (*contractSkill) ActivateTools() []string { return nil }
func TestSupervisorCompletionUsesPermissionsTagsAndSkills(t *testing.T) {
	deps := isolatedModuleDeps(t)
	registry := deps.Tools.(isolatedToolCapabilities).Registry
	for _, v := range []tool.Tool{&contractTool{}, &contractPrivateTool{}, &contractSkill{}} {
		if err := registry.Register(v); err != nil {
			t.Fatal(err)
		}
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
		{user, "@tool:contract_private", "@tool:contract_private", false},
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

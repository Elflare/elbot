// 实现 Skill 修改工具，准备并提交修改内容。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/fileops"
	"elbot/internal/llm"
)

const (
	ModifyElSkillName = "modify_el_skill"

	elSkillTargetElyph      = "skill_elyph"
	elSkillTargetCodeSource = "code_source"
)

type ModifyElSkillTool struct {
	manager *Manager
}

type modifyElSkillArgs struct {
	Name             string         `json:"name"`
	Target           string         `json:"target"`
	Encoding         string         `json:"encoding"`
	ExpectedRevision string         `json:"expected_revision"`
	ContextLines     int            `json:"context_lines"`
	Edits            []fileops.Edit `json:"edits"`
}

type modifyElSkillPreview struct {
	Name   string                // 准备时选定的技能名称。
	Target string                // 技能定义或 Go 源码。
	Path   string                // 技能内的目标路径。
	File   *fileops.PreparedFile // 公共文件层固定内容、目标和 revision。
}

func NewModifyElSkillTool(manager *Manager) ModifyElSkillTool {
	return ModifyElSkillTool{manager: manager}
}

func (ModifyElSkillTool) Name() string { return ModifyElSkillName }

func (ModifyElSkillTool) Info() toolshare.Info {
	return toolshare.NewBuilder(ModifyElSkillName).
		Description("修改 ElBot 原生 EL Skill 的 SKILL.elyph 或 main.go；技能定义会校验 ELyph，写入后需 finalize_el_skill 重载，源码只写入文件，完成后用 finalize_el_skill 格式化和编译。").
		DependsOn(FinalizeElSkillName, "read_el_skill").
		Source(toolshare.SourceBuiltin).
		Risk(toolshare.RiskHigh).
		BuildInfo()
}

func (ModifyElSkillTool) Schema() llm.ToolSchema {
	editProperties := fileops.EditOperationProperties()
	return llm.ToolSchema{
		Name:        ModifyElSkillName,
		Description: "修改 ElBot 原生 EL Skill 文件。target 可选：skill_elyph 修改 SKILL.elyph；code_source 修改 main.go；默认 skill_elyph。使用与 edit_file 相同的精确文本、anchor、行号插入/删除协议；所有目标基于编辑前原文解析，确认前自动预检并生成 diff。code_source 只写源码并返回 diff，不会格式化、编译或 reload；完成后调用 finalize_el_skill 检查。skill_elyph 会校验 ELyph，写入后需 finalize_el_skill 重载。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":              map[string]any{"type": "string", "description": "skill 名称。"},
				"target":            map[string]any{"type": "string", "enum": []string{elSkillTargetElyph, elSkillTargetCodeSource}, "description": "修改目标：skill_elyph 表示 SKILL.elyph；code_source 表示 main.go。默认 skill_elyph。"},
				"encoding":          map[string]any{"type": "string", "description": "文本编码，默认 auto；非 UTF-8 文件应显式传入 gb18030、gbk、big5、shift_jis 等。"},
				"expected_revision": map[string]any{"type": "string", "description": "可选，编辑前读取到的 revision；用于防止外部并发修改。"},
				"context_lines":     map[string]any{"type": "integer", "description": "diff 上下文行数，默认 3，范围 0-20。确认前自动预检和实际写入结果都会使用该上下文行数。"},
				"edits":             map[string]any{"type": "array", "description": "批量编辑列表；所有目标均基于编辑前原文解析。已有文件的 replace/insert/delete/overwrite 必须提供 expected_revision。", "items": map[string]any{"type": "object", "properties": editProperties, "required": []string{"operation"}}},
			},
			"required": []string{"name", "edits"},
		},
	}
}

func (t ModifyElSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	preview, err := t.preview(ctx, req)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "技能：%s\n", preview.Name)
	fmt.Fprintf(&b, "目标：%s\n", preview.Target)
	fmt.Fprintf(&b, "文件：%s\n", preview.Path)
	b.WriteString("模式：确认后写入；确认前已自动预检\n")
	b.WriteString("预检 diff:\n")
	b.WriteString(preview.File.Preview().Diff)
	if !strings.HasSuffix(preview.File.Preview().Diff, "\n") {
		b.WriteString("\n")
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, strings.TrimRight(b.String(), "\n"), func(ctx context.Context) (*toolshare.Result, string, error) {
		return t.call(ctx, preview)
	}), nil
}

func (t ModifyElSkillTool) preview(ctx context.Context, req toolshare.CallRequest) (modifyElSkillPreview, error) {
	if err := ctx.Err(); err != nil {
		return modifyElSkillPreview{}, err
	}
	var args modifyElSkillArgs
	if len(req.Arguments) > 0 {
		if err := decodeModifyElSkillArgs(req.Arguments, &args); err != nil {
			return modifyElSkillPreview{}, fmt.Errorf("parse modify_el_skill arguments: %w", err)
		}
	}
	name := strings.TrimSpace(args.Name)
	path, target, err := elSkillTargetPath(t.manager, name, strings.TrimSpace(args.Target))
	if err != nil {
		return modifyElSkillPreview{}, err
	}
	if len(args.Edits) == 0 {
		return modifyElSkillPreview{}, fmt.Errorf("edits is required")
	}
	file, err := fileops.PrepareFileEdit(ctx, fileops.EditRequest{Path: path, Encoding: args.Encoding, ExpectedRevision: args.ExpectedRevision, ContextLines: args.ContextLines, Edits: args.Edits}, func(next string) error {
		if target == elSkillTargetElyph {
			_, _, err := elyph.ValidateSkill(next, name)
			return err
		}
		return nil
	})
	if err != nil {
		return modifyElSkillPreview{}, err
	}
	return modifyElSkillPreview{Name: name, Target: target, Path: path, File: file}, nil
}

func decodeModifyElSkillArgs(raw json.RawMessage, args *modifyElSkillArgs) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(args)
}

func elSkillTargetPath(manager *Manager, name, target string) (string, string, error) {
	if err := validateSkillName(name); err != nil {
		return "", "", err
	}
	if target == "" {
		target = elSkillTargetElyph
	}
	filename := ""
	switch target {
	case elSkillTargetElyph:
		filename = elyph.SkillFileName
	case elSkillTargetCodeSource:
		filename = "main.go"
	default:
		return "", "", fmt.Errorf("invalid target %q; use %q or %q", target, elSkillTargetElyph, elSkillTargetCodeSource)
	}
	path := filepath.Join(manager.root, "go", name, filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", "", fmt.Errorf("EL skill %s %q not found", target, name)
	} else if err != nil {
		return "", "", fmt.Errorf("stat %s: %w", filename, err)
	}
	return path, target, nil
}

func (t ModifyElSkillTool) call(ctx context.Context, preview modifyElSkillPreview) (*toolshare.Result, string, error) {
	t.manager.writeMu.Lock()
	defer t.manager.writeMu.Unlock()
	result, err := preview.File.Commit(ctx)
	if err != nil {
		return nil, err.Error(), err
	}
	message := "源码已写入但尚未格式化/编译；完成修改后调用 finalize_el_skill。"
	if preview.Target == elSkillTargetElyph {
		message = "SKILL.elyph 已写入但尚未 reload；完成修改后调用 finalize_el_skill。"
	}
	content := fmt.Sprintf("modified EL skill %s target %s\nrevision_after: %s\ndiff:\n%s\n\n%s", preview.Name, preview.Target, result.RevisionAfter, result.Diff, message)
	return &toolshare.Result{Content: content, NeedsElyphRules: preview.Target == elSkillTargetElyph}, fmt.Sprintf("modified=%s target=%s\ndiff:\n%s", preview.Name, preview.Target, result.Diff), nil
}

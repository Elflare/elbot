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
		Description("编辑EL Skill→diff；skill_elyph校验ELyph，code_source仅写源码；完成修改→finalize_el_skill检查并reload。").
		DependsOn(FinalizeElSkillName, "read_el_skill").
		Source(toolshare.SourceBuiltin).
		Risk(toolshare.RiskHigh).
		BuildInfo()
}

func (t ModifyElSkillTool) Schema() llm.ToolSchema {
	editProperties := fileops.EditOperationProperties()
	return llm.ToolSchema{
		Name:        ModifyElSkillName,
		Description: t.Info().Description,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":              map[string]any{"type": "string", "description": "Skill名称。"},
				"target":            map[string]any{"type": "string", "enum": []string{elSkillTargetElyph, elSkillTargetCodeSource}, "description": "skill_elyph→SKILL.elyph(默认)，code_source→main.go。"},
				"encoding":          map[string]any{"type": "string", "description": "默认auto；非UTF-8显式指定，如gb18030、big5、shift_jis。"},
				"expected_revision": map[string]any{"type": "string", "description": "含replace/insert/delete/overwrite⇒必填；取read_el_skill的revision。其他操作可传以校验版本。"},
				"context_lines":     map[string]any{"type": "integer", "description": "diff上下文行数；0/省略→3，负数→0，上限20。"},
				"edits":             map[string]any{"type": "array", "minItems": 1, "description": "定位基于原文；修改范围不重叠，插入点不在其内部；overwrite独占批次。", "items": map[string]any{"type": "object", "properties": editProperties, "required": []string{"operation"}}},
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

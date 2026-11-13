// 实现 Skill 修改工具，准备并提交修改内容。
package skill

import (
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
)

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

func (t ModifyElSkillTool) Definition() toolshare.ToolDefinition {
	builder := toolshare.NewBuilder(ModifyElSkillName).
		Description("编辑EL Skill→diff；skill_elyph校验ELyph，code_source仅写源码；完成修改→finalize_el_skill检查并reload。").
		DependsOn(FinalizeElSkillName, "read_el_skill").
		Risk(toolshare.RiskHigh)

	editProperties := fileops.EditOperationProperties()

	definition := builder.Build(t.PrepareCall)
	definition.Parameters = map[string]any{
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
	}
	return definition
}

func (t ModifyElSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	prepared, err := t.preview(ctx, req)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*toolshare.PreparedCall]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	preview := prepared.Data
	filePreview := preview.File.Preview()
	details := []toolshare.ConfirmationDetailBlock{toolshare.FileDiffConfirmationDetail(
		filePreview.Diff,
		filePreview.DiffAvailable,
		fmt.Sprintf("技能：%s\n目标：%s\n文件：%s\n模式：确认后写入；确认前已自动预检\n预检 diff:", preview.Name, preview.Target, preview.Path),
	)}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		return t.call(ctx, preview)
	})}, nil
}

func (t ModifyElSkillTool) preview(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[modifyElSkillPreview], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[modifyElSkillPreview]{}, err
	}
	var args modifyElSkillArgs
	if len(req.Arguments) > 0 {
		if err := decodeModifyElSkillArgs(req.Arguments, &args); err != nil {
			return operationresult.Result[modifyElSkillPreview]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse modify_el_skill arguments: %v", err)}, nil
		}
	}
	name := strings.TrimSpace(args.Name)
	selected, target, err := elSkillTargetPath(t.manager, name, strings.TrimSpace(args.Target))
	if err != nil {
		return operationresult.Result[modifyElSkillPreview]{}, err
	}
	if selected.Status != operationresult.Success {
		return operationresult.Result[modifyElSkillPreview]{Status: selected.Status, Message: selected.Message}, nil
	}
	path := selected.Data
	if len(args.Edits) == 0 {
		return operationresult.Result[modifyElSkillPreview]{Status: operationresult.InvalidArguments, Message: "edits is required"}, nil
	}
	file, err := fileops.PrepareFileEdit(ctx, fileops.EditRequest{Path: path, Encoding: args.Encoding, ExpectedRevision: args.ExpectedRevision, ContextLines: args.ContextLines, Edits: args.Edits}, func(next string) (operationresult.Result[struct{}], error) {
		if target == elSkillTargetElyph {
			if _, _, err := elyph.ValidateSkill(next, name); err != nil {
				return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
			}
		}
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	})
	if err != nil {
		return operationresult.Result[modifyElSkillPreview]{}, err
	}
	if file.Status != operationresult.Success {
		return operationresult.Result[modifyElSkillPreview]{Status: file.Status, Message: file.Message}, nil
	}
	return operationresult.Result[modifyElSkillPreview]{Status: operationresult.Success, Data: modifyElSkillPreview{Name: name, Target: target, Path: path, File: file.Data}}, nil
}

func decodeModifyElSkillArgs(raw json.RawMessage, args *modifyElSkillArgs) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(args)
}

func elSkillTargetPath(manager *Manager, name, target string) (operationresult.Result[string], string, error) {
	if err := validateSkillName(name); err != nil {
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: err.Error()}, "", nil
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
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("invalid target %q; use %q or %q", target, elSkillTargetElyph, elSkillTargetCodeSource)}, "", nil
	}
	path := filepath.Join(manager.root, "go", name, filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: fmt.Sprintf("EL skill %s %q not found", target, name)}, "", nil
	} else if err != nil {
		return operationresult.Result[string]{}, "", fmt.Errorf("stat %s: %w", filename, err)
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: path}, target, nil
}

func (t ModifyElSkillTool) call(ctx context.Context, preview modifyElSkillPreview) (operationresult.Result[*toolshare.Result], string, error) {
	t.manager.writeMu.Lock()
	defer t.manager.writeMu.Unlock()
	committed, err := preview.File.Commit(ctx)
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, err.Error(), err
	}
	if committed.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: committed.Status, Message: committed.Message}, "skill edit rejected: " + committed.Message, nil
	}
	result := committed.Data
	message := "源码已写入但尚未格式化/编译；完成修改后调用 finalize_el_skill。"
	if preview.Target == elSkillTargetElyph {
		message = "SKILL.elyph 已写入但尚未 reload；完成修改后调用 finalize_el_skill。"
	}
	content := fmt.Sprintf("modified EL skill %s target %s\nrevision_after: %s\ndiff:\n%s\n\n%s", preview.Name, preview.Target, result.RevisionAfter, result.Diff, message)
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: content, NeedsElyphRules: preview.Target == elSkillTargetElyph}}, fmt.Sprintf("modified=%s target=%s revision_after=%s", preview.Name, preview.Target, result.RevisionAfter), nil
}

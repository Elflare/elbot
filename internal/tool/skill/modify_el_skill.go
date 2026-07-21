package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
)

const (
	ReadElSkillName   = "read_el_skill"
	ModifyElSkillName = "modify_el_skill"

	elSkillTargetElyph      = "skill_elyph"
	elSkillTargetCodeSource = "code_source"
)

type ReadElSkillTool struct {
	Manager *Manager
}

type ModifyElSkillTool struct {
	Manager *Manager
}

type readElSkillArgs struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
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
	Name   string
	Target string
	Path   string
	Result fileops.EditResult
	Next   string
	Diff   string
}

func NewReadElSkillTool(manager *Manager) ReadElSkillTool {
	return ReadElSkillTool{Manager: manager}
}

func NewModifyElSkillTool(manager *Manager) ModifyElSkillTool {
	return ModifyElSkillTool{Manager: manager}
}

func (ReadElSkillTool) Name() string { return ReadElSkillName }

func (ReadElSkillTool) Info() tool.Info {
	return tool.NewBuilder(ReadElSkillName).
		Description("按行读取 ElBot 原生 EL Skill 的 SKILL.elyph 或 main.go，返回 revision 和 1-based 行号，供修改前定位使用。").
		DependsOn("modify_el_skill").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskMedium).
		BuildInfo()
}

func (ReadElSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
		Name:        ReadElSkillName,
		Description: "按行读取 ElBot 原生 EL Skill 文件并返回 revision。target 可选：skill_elyph 读取 SKILL.elyph；code_source 读取 main.go。默认读取 SKILL.elyph。start_line/end_line 为 1-based，可省略以读取全文。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "skill 名称。"},
				"target":     map[string]any{"type": "string", "enum": []string{elSkillTargetElyph, elSkillTargetCodeSource}, "description": "读取目标：skill_elyph 表示 SKILL.elyph；code_source 表示 main.go。默认 skill_elyph。"},
				"start_line": map[string]any{"type": "integer", "description": "可选，1-based 起始行，默认 1。"},
				"end_line":   map[string]any{"type": "integer", "description": "可选，1-based 结束行，默认文件末尾。"},
			},
			"required": []string{"name"},
		},
	}}
}

func (t ReadElSkillTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var args readElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse read_el_skill arguments: %w", err)
		}
	}
	path, _, err := elSkillTargetPath(t.Manager, strings.TrimSpace(args.Name), strings.TrimSpace(args.Target))
	if err != nil {
		return nil, err
	}
	file, err := fileops.ReadFile(path, "")
	if err != nil {
		return nil, err
	}
	lines := fileops.SplitLines(file.Text)
	start := args.StartLine
	if start <= 0 {
		start = 1
	}
	end := args.EndLine
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > end+1 || start > len(lines)+1 {
		return nil, fmt.Errorf("start_line %d is out of range", args.StartLine)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "revision: %s\n", fileops.ContentRevision(file.Bytes))
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
	}
	return &tool.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

func (ModifyElSkillTool) Name() string { return ModifyElSkillName }

func (ModifyElSkillTool) Info() tool.Info {
	return tool.NewBuilder(ModifyElSkillName).
		Description("修改 ElBot 原生 EL Skill 的 SKILL.elyph 或 main.go；技能定义会校验 ELyph 并 reload，源码只写入文件，完成后用 finalize_el_skill 格式化和编译。").
		DependsOn(FinalizeElSkillName, "read_el_skill").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskHigh).
		BuildInfo()
}

func (ModifyElSkillTool) Schema() llm.ToolSchema {
	editProperties := fileops.EditOperationProperties()
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
		Name:        ModifyElSkillName,
		Description: "修改 ElBot 原生 EL Skill 文件。target 可选：skill_elyph 修改 SKILL.elyph；code_source 修改 main.go；默认 skill_elyph。使用与 edit_file 相同的精确文本、anchor、行号插入/删除协议；所有目标基于编辑前原文解析，确认前自动预检并生成 diff。code_source 只写源码并返回 diff，不会格式化、编译或 reload；完成后调用 finalize_el_skill 检查。skill_elyph 会校验 ELyph 并 reload。",
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
	}}
}

func (t ModifyElSkillTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	_, err := t.preview(ctx, req)
	return err
}

func (t ModifyElSkillTool) RiskDetail(ctx context.Context, req tool.CallRequest) (string, error) {
	preview, err := t.preview(ctx, req)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "技能：%s\n", preview.Name)
	fmt.Fprintf(&b, "目标：%s\n", preview.Target)
	fmt.Fprintf(&b, "文件：%s\n", preview.Path)
	b.WriteString("模式：确认后写入；确认前已自动预检\n")
	b.WriteString("预检 diff:\n")
	b.WriteString(preview.Diff)
	if !strings.HasSuffix(preview.Diff, "\n") {
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (t ModifyElSkillTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	preview, err := t.preview(ctx, req)
	if err != nil {
		return nil, err
	}
	switch preview.Target {
	case elSkillTargetElyph:
		return t.modifyElyph(preview)
	case elSkillTargetCodeSource:
		return t.modifyCodeSource(preview)
	default:
		return nil, fmt.Errorf("unsupported target %q", preview.Target)
	}
}

func (t ModifyElSkillTool) preview(ctx context.Context, req tool.CallRequest) (modifyElSkillPreview, error) {
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
	path, target, err := elSkillTargetPath(t.Manager, name, strings.TrimSpace(args.Target))
	if err != nil {
		return modifyElSkillPreview{}, err
	}
	if len(args.Edits) == 0 {
		return modifyElSkillPreview{}, fmt.Errorf("edits is required")
	}
	result, err := fileops.EditFile(path, args.Encoding, args.ExpectedRevision, false, true, args.ContextLines, args.Edits)
	if err != nil {
		return modifyElSkillPreview{}, err
	}
	next, _, _, err := fileops.DecodeBytes(result.NewBytes, result.Encoding)
	if err != nil {
		return modifyElSkillPreview{}, err
	}
	if target == elSkillTargetElyph {
		if _, _, err := elyph.ValidateSkill(next, name); err != nil {
			return modifyElSkillPreview{}, err
		}
	}
	return modifyElSkillPreview{Name: name, Target: target, Path: path, Result: result, Next: next, Diff: result.Diff}, nil
}

func (t ModifyElSkillTool) modifyElyph(preview modifyElSkillPreview) (*tool.Result, error) {
	if err := fileops.AtomicWriteFile(preview.Path, preview.Result.NewBytes, existingFileMode(preview.Path)); err != nil {
		return nil, err
	}
	return &tool.Result{Content: fmt.Sprintf("modified EL skill %s target %s\nrevision_after: %s\ndiff:\n%s\n\nSKILL.elyph 已写入但尚未 reload；完成修改后调用 finalize_el_skill。", preview.Name, elSkillTargetElyph, preview.Result.RevisionAfter, preview.Diff)}, nil
}

func (t ModifyElSkillTool) modifyCodeSource(preview modifyElSkillPreview) (*tool.Result, error) {
	if err := fileops.AtomicWriteFile(preview.Path, preview.Result.NewBytes, existingFileMode(preview.Path)); err != nil {
		return nil, err
	}
	return &tool.Result{Content: fmt.Sprintf("modified EL skill %s target %s\nrevision_after: %s\ndiff:\n%s\n\n源码已写入但尚未格式化/编译；完成修改后调用 finalize_el_skill。", preview.Name, elSkillTargetCodeSource, preview.Result.RevisionAfter, preview.Diff)}, nil
}

func decodeModifyElSkillArgs(raw json.RawMessage, args *modifyElSkillArgs) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(args)
}

func existingFileMode(path string) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode()
	}
	return 0644
}

func elSkillTargetPath(manager *Manager, name, target string) (string, string, error) {
	if manager == nil || manager.Registry == nil {
		return "", "", fmt.Errorf("skill manager is not configured")
	}
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
	path := filepath.Join(manager.Root, "go", name, filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", "", fmt.Errorf("EL skill %s %q not found", target, name)
	} else if err != nil {
		return "", "", fmt.Errorf("stat %s: %w", filename, err)
	}
	return path, target, nil
}

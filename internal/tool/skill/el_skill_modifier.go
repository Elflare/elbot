package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Name    string            `json:"name"`
	Target  string            `json:"target"`
	Content string            `json:"content"`
	Patches []modifyLinePatch `json:"patches"`
}

type modifyLinePatch struct {
	StartLine int      `json:"start_line"`
	EndLine   int      `json:"end_line"`
	NewLines  []string `json:"new_lines"`
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
		Description("按行读取 ElBot 原生 EL Skill 的 SKILL.elyph 或 main.go，返回 1-based 行号，供修改前定位使用。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskMedium).
		BuildInfo()
}

func (ReadElSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
		Name:        ReadElSkillName,
		Description: "按行读取 ElBot 原生 EL Skill 文件。target 可选：skill_elyph 读取 SKILL.elyph；code_source 读取 main.go。默认读取 SKILL.elyph。start_line/end_line 为 1-based，可省略以读取全文。",
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
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
	}
	return &tool.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

func (ModifyElSkillTool) Name() string { return ModifyElSkillName }

func (ModifyElSkillTool) Info() tool.Info {
	return tool.NewBuilder(ModifyElSkillName).
		Description("修改 ElBot 原生 EL Skill 的 SKILL.elyph 或 main.go；技能定义会校验 ELyph 并 reload，源码只写入文件，完成后用 finalize_el_skill 格式化和编译。").
		DependsOn(FinalizeElSkillName).
		Source(tool.SourceBuiltin).
		Risk(tool.RiskHigh).
		BuildInfo()
}

func (ModifyElSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
		Name:        ModifyElSkillName,
		Description: "修改 ElBot 原生 EL Skill 文件。target 可选：skill_elyph 修改 SKILL.elyph；code_source 修改 main.go；默认 skill_elyph。content 用于全量写入；patches 用原文件 1-based 行号替换整行范围。content 与 patches 必须二选一。code_source 只写源码并返回 diff，不会格式化、编译或 reload；完成后调用 finalize_el_skill 检查。skill_elyph 会校验 ELyph 并 reload。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "skill 名称。"},
				"target":  map[string]any{"type": "string", "enum": []string{elSkillTargetElyph, elSkillTargetCodeSource}, "description": "修改目标：skill_elyph 表示 SKILL.elyph；code_source 表示 main.go。默认 skill_elyph。"},
				"content": map[string]any{"type": "string", "description": "可选，完整新的文件内容。"},
				"patches": map[string]any{"type": "array", "description": "可选，按原文件行号替换整行范围；new_lines 为空数组表示删除。", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"start_line": map[string]any{"type": "integer", "description": "1-based 起始行，包含。"},
						"end_line":   map[string]any{"type": "integer", "description": "1-based 结束行，包含。"},
						"new_lines":  map[string]any{"type": "array", "description": "替换后的行数组，每个元素不包含换行符。", "items": map[string]any{"type": "string"}},
					},
					"required": []string{"start_line", "end_line", "new_lines"},
				}},
			},
			"required": []string{"name"},
		},
	}}
}

func (t ModifyElSkillTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args modifyElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse modify_el_skill arguments: %w", err)
		}
	}
	name := strings.TrimSpace(args.Name)
	path, target, err := elSkillTargetPath(t.Manager, name, strings.TrimSpace(args.Target))
	if err != nil {
		return nil, err
	}
	hasContent := args.Content != ""
	hasPatches := len(args.Patches) > 0
	if hasContent == hasPatches {
		return nil, fmt.Errorf("provide exactly one of content or patches")
	}
	file, err := fileops.ReadFile(path, "")
	if err != nil {
		return nil, err
	}
	next := args.Content
	if hasPatches {
		next, err = applyLinePatches(fileops.SplitLines(file.Text), args.Patches)
		if err != nil {
			return nil, err
		}
	}
	switch target {
	case elSkillTargetElyph:
		return t.modifyElyph(ctx, name, path, file, next)
	case elSkillTargetCodeSource:
		return t.modifyCodeSource(name, path, file, next)
	default:
		return nil, fmt.Errorf("unsupported target %q", target)
	}
}

func (t ModifyElSkillTool) modifyElyph(ctx context.Context, name, path string, file fileops.File, next string) (*tool.Result, error) {
	next = normalizeSkillContent(next)
	if _, err := elyph.ParseSkill(next, name); err != nil {
		return nil, err
	}
	diff := fileops.UnifiedDiff(path, fileops.SplitLines(fileops.NormalizeEditText(file.Text)), fileops.SplitLines(fileops.NormalizeEditText(next)), 3)
	if _, err := fileops.WriteTextFile(path, file, next); err != nil {
		return nil, err
	}
	if err := t.Manager.Reload(ctx); err != nil {
		return nil, fmt.Errorf("EL skill modified but reload failed: %w", err)
	}
	return &tool.Result{Content: fmt.Sprintf("modified EL skill %s target %s\ndiff:\n%s", name, elSkillTargetElyph, diff)}, nil
}

func (t ModifyElSkillTool) modifyCodeSource(name, path string, file fileops.File, next string) (*tool.Result, error) {
	next = normalizeSkillContent(next)
	diff := fileops.UnifiedDiff(path, fileops.SplitLines(fileops.NormalizeEditText(file.Text)), fileops.SplitLines(fileops.NormalizeEditText(next)), 3)
	if _, err := fileops.WriteTextFile(path, file, next); err != nil {
		return nil, err
	}
	return &tool.Result{Content: fmt.Sprintf("modified EL skill %s target %s\ndiff:\n%s\n\n源码已写入但尚未格式化/编译；完成修改后调用 finalize_el_skill。", name, elSkillTargetCodeSource, diff)}, nil
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

func splitSkillLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return []string{}
	}
	return strings.Split(text, "\n")
}

func applyLinePatches(lines []string, patches []modifyLinePatch) (string, error) {
	sorted := append([]modifyLinePatch(nil), patches...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartLine < sorted[j].StartLine })
	previousEnd := 0
	for _, patch := range sorted {
		if patch.StartLine < 1 || patch.EndLine < patch.StartLine || patch.EndLine > len(lines) {
			return "", fmt.Errorf("invalid patch range %d-%d", patch.StartLine, patch.EndLine)
		}
		if patch.StartLine <= previousEnd {
			return "", fmt.Errorf("patch ranges must not overlap")
		}
		for _, line := range patch.NewLines {
			if strings.ContainsAny(line, "\r\n") {
				return "", fmt.Errorf("new_lines must not contain line breaks")
			}
		}
		previousEnd = patch.EndLine
	}
	for i := len(sorted) - 1; i >= 0; i-- {
		patch := sorted[i]
		start := patch.StartLine - 1
		end := patch.EndLine
		next := make([]string, 0, len(lines)-(end-start)+len(patch.NewLines))
		next = append(next, lines[:start]...)
		next = append(next, patch.NewLines...)
		next = append(next, lines[end:]...)
		lines = next
	}
	return strings.Join(lines, "\n"), nil
}

func normalizeSkillContent(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.TrimRight(text, "\n") + "\n"
}

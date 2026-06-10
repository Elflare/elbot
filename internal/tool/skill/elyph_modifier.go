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
)

const (
	ReadElSkillName   = "read_el_skill"
	ModifyElSkillName = "modify_el_skill"
)

type ReadElSkillTool struct {
	Manager *Manager
}

type ModifyElSkillTool struct {
	Manager *Manager
}

type readElSkillArgs struct {
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type modifyElSkillArgs struct {
	Name    string            `json:"name"`
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
		Description("按行读取 ElBot 原生 ELyph skill 的 SKILL.elyph，返回 1-based 行号，供修改前定位使用。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskLow).
		BuildInfo()
}

func (ReadElSkillTool) Schema() llm.ToolSchema {
	return tool.NewBuilder(ReadElSkillName).
		Description("按行读取 ElBot 原生 ELyph skill 的 SKILL.elyph。start_line/end_line 为 1-based，可省略以读取全文。").
		String("name", "skill 名称。", tool.Required()).
		Integer("start_line", "可选，1-based 起始行，默认 1。").
		Integer("end_line", "可选，1-based 结束行，默认文件末尾。").
		BuildSchema()
}

func (t ReadElSkillTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args readElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse read_el_skill arguments: %w", err)
		}
	}
	path, err := elSkillPath(t.Manager, strings.TrimSpace(args.Name))
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", elyph.SkillFileName, err)
	}
	lines := splitSkillLines(string(data))
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
		Description("修改 ElBot 原生 ELyph skill 的 SKILL.elyph，支持完整 content 覆盖或按行 patch；写入前严格校验 ELyph 语法。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskHigh).
		BuildInfo()
}

func (ModifyElSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
		Name:        ModifyElSkillName,
		Description: "修改 ElBot 原生 ELyph skill 的 SKILL.elyph。content 用于全量写入；patches 用原文件 1-based 行号替换整行范围。content 与 patches 必须二选一。修改后必须通过 ELyph 语法校验才会写入。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "skill 名称。"},
				"content": map[string]any{"type": "string", "description": "可选，完整新的 SKILL.elyph 内容。"},
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
	path, err := elSkillPath(t.Manager, name)
	if err != nil {
		return nil, err
	}
	hasContent := args.Content != ""
	hasPatches := len(args.Patches) > 0
	if hasContent == hasPatches {
		return nil, fmt.Errorf("provide exactly one of content or patches")
	}
	currentData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", elyph.SkillFileName, err)
	}
	next := args.Content
	if hasPatches {
		next, err = applyLinePatches(splitSkillLines(string(currentData)), args.Patches)
		if err != nil {
			return nil, err
		}
	}
	next = normalizeSkillContent(next)
	if _, err := elyph.ParseSkill(next, name); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", elyph.SkillFileName, err)
	}
	if err := t.Manager.Reload(ctx); err != nil {
		return nil, fmt.Errorf("ELyph skill modified but reload failed: %w", err)
	}
	return &tool.Result{Content: fmt.Sprintf("modified ELyph skill %s", name)}, nil
}

func elSkillPath(manager *Manager, name string) (string, error) {
	if manager == nil || manager.Registry == nil {
		return "", fmt.Errorf("skill manager is not configured")
	}
	if err := validateSkillName(name); err != nil {
		return "", err
	}
	path := filepath.Join(manager.Root, "go", name, elyph.SkillFileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("ELyph skill %q not found", name)
	} else if err != nil {
		return "", fmt.Errorf("stat %s: %w", elyph.SkillFileName, err)
	}
	return path, nil
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

package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
)

const (
	ReadGoSkillName   = "read_go_skill"
	ModifyGoSkillName = "modify_go_skill"

	goSkillTargetElyph      = "skill_elyph"
	goSkillTargetCodeSource = "code_source"
)

type ReadGoSkillTool struct {
	Manager *Manager
}

type ModifyGoSkillTool struct {
	Manager *Manager
}

type readGoSkillArgs struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type modifyGoSkillArgs struct {
	Name      string            `json:"name"`
	Target    string            `json:"target"`
	Content   string            `json:"content"`
	Patches   []modifyLinePatch `json:"patches"`
	TimeoutMS int               `json:"timeout_ms"`
}

func NewReadGoSkillTool(manager *Manager) ReadGoSkillTool {
	return ReadGoSkillTool{Manager: manager}
}

func NewModifyGoSkillTool(manager *Manager) ModifyGoSkillTool {
	return ModifyGoSkillTool{Manager: manager}
}

func (ReadGoSkillTool) Name() string { return ReadGoSkillName }

func (ReadGoSkillTool) Info() tool.Info {
	return tool.NewBuilder(ReadGoSkillName).
		Description("按行读取 ElBot 原生 Go skill 的 SKILL.elyph 或 main.go，返回 1-based 行号，供修改前定位使用。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskMedium).
		Hidden().
		BuildInfo()
}

func (ReadGoSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
		Name:        ReadGoSkillName,
		Description: "按行读取 ElBot 原生 Go skill 文件。target 必填：skill_elyph 读取 SKILL.elyph；code_source 读取 main.go。start_line/end_line 为 1-based，可省略以读取全文。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "skill 名称。"},
				"target":     map[string]any{"type": "string", "enum": []string{goSkillTargetElyph, goSkillTargetCodeSource}, "description": "读取目标：skill_elyph 表示 SKILL.elyph；code_source 表示 main.go。"},
				"start_line": map[string]any{"type": "integer", "description": "可选，1-based 起始行，默认 1。"},
				"end_line":   map[string]any{"type": "integer", "description": "可选，1-based 结束行，默认文件末尾。"},
			},
			"required": []string{"name", "target"},
		},
	}}
}

func (t ReadGoSkillTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var args readGoSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse read_go_skill arguments: %w", err)
		}
	}
	path, _, err := goSkillTargetPath(t.Manager, strings.TrimSpace(args.Name), strings.TrimSpace(args.Target))
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

func (ModifyGoSkillTool) Name() string { return ModifyGoSkillName }

func (ModifyGoSkillTool) Info() tool.Info {
	return tool.NewBuilder(ModifyGoSkillName).
		Description("修改 ElBot 原生 Go skill 的 SKILL.elyph 或 main.go；技能定义会校验 ELyph 并 reload，源码会 gofmt、go build 并 reload。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskHigh).
		Hidden().
		BuildInfo()
}

func (ModifyGoSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
		Name:        ModifyGoSkillName,
		Description: "修改 ElBot 原生 Go skill 文件。target 必填：skill_elyph 修改 SKILL.elyph；code_source 修改 main.go。content 用于全量写入；patches 用原文件 1-based 行号替换整行范围。content 与 patches 必须二选一。code_source 会自动 gofmt、go build 并 reload；skill_elyph 会校验 ELyph 并 reload。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "skill 名称。"},
				"target":     map[string]any{"type": "string", "enum": []string{goSkillTargetElyph, goSkillTargetCodeSource}, "description": "修改目标：skill_elyph 表示 SKILL.elyph；code_source 表示 main.go。"},
				"content":    map[string]any{"type": "string", "description": "可选，完整新的文件内容。"},
				"timeout_ms": map[string]any{"type": "integer", "description": "可选，仅 code_source 编译超时时间，默认 60000。"},
				"patches": map[string]any{"type": "array", "description": "可选，按原文件行号替换整行范围；new_lines 为空数组表示删除。", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"start_line": map[string]any{"type": "integer", "description": "1-based 起始行，包含。"},
						"end_line":   map[string]any{"type": "integer", "description": "1-based 结束行，包含。"},
						"new_lines":  map[string]any{"type": "array", "description": "替换后的源码行数组，每个元素不包含换行符。", "items": map[string]any{"type": "string"}},
					},
					"required": []string{"start_line", "end_line", "new_lines"},
				}},
			},
			"required": []string{"name", "target"},
		},
	}}
}

func (t ModifyGoSkillTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args modifyGoSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse modify_go_skill arguments: %w", err)
		}
	}
	name := strings.TrimSpace(args.Name)
	path, target, err := goSkillTargetPath(t.Manager, name, strings.TrimSpace(args.Target))
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
		edits, err := goSkillPatchEdits(fileops.SplitLines(file.Text), args.Patches)
		if err != nil {
			return nil, err
		}
		next, err = fileops.ApplyEdits(file.Text, edits)
		if err != nil {
			return nil, err
		}
	}
	switch target {
	case goSkillTargetElyph:
		return t.modifyElyph(ctx, name, path, file, next)
	case goSkillTargetCodeSource:
		return t.modifyCodeSource(ctx, name, path, file, next, args.TimeoutMS)
	default:
		return nil, fmt.Errorf("unsupported target %q", target)
	}
}

func (t ModifyGoSkillTool) modifyElyph(ctx context.Context, name, path string, file fileops.File, next string) (*tool.Result, error) {
	next = normalizeSkillContent(next)
	if _, err := elyph.ParseSkill(next, name); err != nil {
		return nil, err
	}
	diff := fileops.UnifiedDiff(path, fileops.SplitLines(fileops.NormalizeEditText(file.Text)), fileops.SplitLines(fileops.NormalizeEditText(next)), 3)
	if _, err := fileops.WriteTextFile(path, file, next); err != nil {
		return nil, err
	}
	if err := t.Manager.Reload(ctx); err != nil {
		return nil, fmt.Errorf("Go skill ELyph modified but reload failed: %w", err)
	}
	return &tool.Result{Content: fmt.Sprintf("modified Go skill %s target %s\ndiff:\n%s", name, goSkillTargetElyph, diff)}, nil
}

func (t ModifyGoSkillTool) modifyCodeSource(ctx context.Context, name, path string, file fileops.File, next string, timeoutMS int) (*tool.Result, error) {
	formatted, err := formatGoSource(next)
	if err != nil {
		return nil, err
	}
	if err := validateGoMainSource(formatted); err != nil {
		return nil, err
	}
	diff := fileops.UnifiedDiff(path, fileops.SplitLines(fileops.NormalizeEditText(file.Text)), fileops.SplitLines(fileops.NormalizeEditText(formatted)), 3)
	if _, err := fileops.WriteTextFile(path, file, formatted); err != nil {
		return nil, err
	}
	if err := buildGoSkill(ctx, filepath.Dir(path), name, timeoutMS); err != nil {
		if _, restoreErr := fileops.WriteTextFile(path, file, file.Text); restoreErr != nil {
			return nil, fmt.Errorf("%w\nrestore original source failed: %v", err, restoreErr)
		}
		return nil, err
	}
	if err := t.Manager.Reload(ctx); err != nil {
		return nil, fmt.Errorf("Go skill rebuilt but reload failed: %w", err)
	}
	return &tool.Result{Content: fmt.Sprintf("modified Go skill %s target %s\ndiff:\n%s", name, goSkillTargetCodeSource, diff)}, nil
}

func goSkillTargetPath(manager *Manager, name, target string) (string, string, error) {
	if manager == nil || manager.Registry == nil {
		return "", "", fmt.Errorf("skill manager is not configured")
	}
	if err := validateSkillName(name); err != nil {
		return "", "", err
	}
	if target == "" {
		return "", "", fmt.Errorf("target is required; use %q or %q", goSkillTargetElyph, goSkillTargetCodeSource)
	}
	filename := ""
	switch target {
	case goSkillTargetElyph:
		filename = elyph.SkillFileName
	case goSkillTargetCodeSource:
		filename = "main.go"
	default:
		return "", "", fmt.Errorf("invalid target %q; use %q or %q", target, goSkillTargetElyph, goSkillTargetCodeSource)
	}
	path := filepath.Join(manager.Root, "go", name, filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", "", fmt.Errorf("Go skill %s %q not found", target, name)
	} else if err != nil {
		return "", "", fmt.Errorf("stat %s: %w", filename, err)
	}
	return path, target, nil
}

func goSkillPatchEdits(lines []string, patches []modifyLinePatch) ([]fileops.Edit, error) {
	sorted := append([]modifyLinePatch(nil), patches...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartLine < sorted[j].StartLine })
	previousEnd := 0
	for _, patch := range sorted {
		if patch.StartLine < 1 || patch.EndLine < patch.StartLine || patch.EndLine > len(lines) {
			return nil, fmt.Errorf("invalid patch range %d-%d", patch.StartLine, patch.EndLine)
		}
		if patch.StartLine <= previousEnd {
			return nil, fmt.Errorf("patch ranges must not overlap")
		}
		for _, line := range patch.NewLines {
			if strings.ContainsAny(line, "\r\n") {
				return nil, fmt.Errorf("new_lines must not contain line breaks")
			}
		}
		previousEnd = patch.EndLine
	}
	edits := make([]fileops.Edit, 0, len(sorted))
	for i := len(sorted) - 1; i >= 0; i-- {
		patch := sorted[i]
		operation := "replace"
		if len(patch.NewLines) == 0 {
			operation = "delete"
		}
		edits = append(edits, fileops.Edit{
			Operation: operation,
			StartLine: patch.StartLine,
			EndLine:   fileops.LineNumber{Value: patch.EndLine},
			Content:   strings.Join(patch.NewLines, "\n"),
		})
	}
	return edits, nil
}

func normalizeGoSource(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.TrimRight(text, "\n") + "\n"
}

func formatGoSource(source string) (string, error) {
	formatted, err := format.Source([]byte(normalizeGoSource(source)))
	if err != nil {
		return "", fmt.Errorf("gofmt source: %w", err)
	}
	return string(formatted), nil
}

func validateGoMainSource(source string) error {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, parser.AllErrors)
	if err != nil {
		return fmt.Errorf("parse Go source: %w", err)
	}
	if file.Name == nil || file.Name.Name != "main" {
		return fmt.Errorf("go source must declare package main")
	}
	return nil
}

func buildGoSkill(ctx context.Context, root, name string, timeoutMS int) error {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("system go executable not found; install Go and ensure it is in PATH before rebuilding Go skill")
	}
	binary := name
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	timeout := 60 * time.Second
	if timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, goPath, "build", "-o", binary, ".")
	cmd.Dir = root
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build failed: %w\nstdout:\n%s\nstderr:\n%s", err, truncateOutput(stdout.String()), truncateOutput(stderr.String()))
	}
	return nil
}

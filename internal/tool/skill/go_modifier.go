package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

const (
	ReadGoSkillName   = "read_go_skill"
	ModifyGoSkillName = "modify_go_skill"
)

type ReadGoSkillTool struct {
	Manager *Manager
}

type ModifyGoSkillTool struct {
	Manager *Manager
}

type readGoSkillArgs struct {
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type modifyGoSkillArgs struct {
	Name      string            `json:"name"`
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
		Description("按行读取 ElBot 原生 Go skill 的 main.go，返回 1-based 行号，供修改前定位使用。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskMedium).
		Hidden().
		BuildInfo()
}

func (ReadGoSkillTool) Schema() llm.ToolSchema {
	return tool.NewBuilder(ReadGoSkillName).
		Description("按行读取 ElBot 原生 Go skill 的 main.go。start_line/end_line 为 1-based，可省略以读取全文。").
		String("name", "skill 名称。", tool.Required()).
		Integer("start_line", "可选，1-based 起始行，默认 1。").
		Integer("end_line", "可选，1-based 结束行，默认文件末尾。").
		BuildSchema()
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
	path, err := goSkillSourcePath(t.Manager, strings.TrimSpace(args.Name))
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read main.go: %w", err)
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

func (ModifyGoSkillTool) Name() string { return ModifyGoSkillName }

func (ModifyGoSkillTool) Info() tool.Info {
	return tool.NewBuilder(ModifyGoSkillName).
		Description("修改 ElBot 原生 Go skill 的 main.go，支持完整 content 覆盖或按行 patch；写入后自动 go build 并 reload。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskHigh).
		Hidden().
		BuildInfo()
}

func (ModifyGoSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
		Name:        ModifyGoSkillName,
		Description: "修改 ElBot 原生 Go skill 的 main.go。content 用于全量写入；patches 用原文件 1-based 行号替换整行范围。content 与 patches 必须二选一。写入后自动 go build 并 reload，编译失败会返回 stdout/stderr。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "skill 名称。"},
				"content":    map[string]any{"type": "string", "description": "可选，完整新的 main.go 内容。"},
				"timeout_ms": map[string]any{"type": "integer", "description": "可选，编译超时时间，默认 60000。"},
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
			"required": []string{"name"},
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
	path, err := goSkillSourcePath(t.Manager, name)
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
		return nil, fmt.Errorf("read main.go: %w", err)
	}
	next := args.Content
	if hasPatches {
		next, err = applyLinePatches(splitSkillLines(string(currentData)), args.Patches)
		if err != nil {
			return nil, err
		}
	}
	next = normalizeGoSource(next)
	if err := validateGoSource(next); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return nil, fmt.Errorf("write main.go: %w", err)
	}
	if err := buildGoSkill(ctx, filepath.Dir(path), name, args.TimeoutMS); err != nil {
		return nil, err
	}
	if err := t.Manager.Reload(ctx); err != nil {
		return nil, fmt.Errorf("Go skill rebuilt but reload failed: %w", err)
	}
	return &tool.Result{Content: fmt.Sprintf("modified Go skill %s", name)}, nil
}

func goSkillSourcePath(manager *Manager, name string) (string, error) {
	if manager == nil || manager.Registry == nil {
		return "", fmt.Errorf("skill manager is not configured")
	}
	if err := validateSkillName(name); err != nil {
		return "", err
	}
	path := filepath.Join(manager.Root, "go", name, "main.go")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("Go skill source %q not found", name)
	} else if err != nil {
		return "", fmt.Errorf("stat main.go: %w", err)
	}
	return path, nil
}

func normalizeGoSource(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.TrimRight(text, "\n") + "\n"
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

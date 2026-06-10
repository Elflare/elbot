package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

const CreateElSkillName = "create_el_skill"

var skillNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type CreateElSkillTool struct {
	Manager *Manager
}

type createElSkillArgs struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
	Elyph       string `json:"elyph"`
	GoSource    string `json:"go_source"`
	TimeoutMS   int    `json:"timeout_ms"`
}

func NewCreateElSkillTool(manager *Manager) CreateElSkillTool {
	return CreateElSkillTool{Manager: manager}
}

func (CreateElSkillTool) Name() string { return CreateElSkillName }

func (CreateElSkillTool) Info() tool.Info {
	return tool.NewBuilder(CreateElSkillName).
		Description("创建 ElBot 原生 ELyph skill。只有可复用经验、固定流程或稳定能力才应创建 skill；一次性任务不要创建。可只写 SKILL.elyph，也可附带 Go 源码并通过 go_skill_run 调用。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskHigh).
		BuildInfo()
}

func (CreateElSkillTool) Schema() llm.ToolSchema {
	return tool.NewBuilder(CreateElSkillName).
		Description("创建 ElBot 原生 ELyph skill。工具会写入 SKILL.elyph；提供 go_source 时写入 main.go 并编译 binary。Go skill 后续通过 go_skill_run 调用，skill_name 选择 skill，其余顶层字段会作为业务参数 JSON 写入 stdin。").
		String("name", "skill 名称，也是目录名和 binary 名；使用小写字母、数字、下划线或短横线。", tool.Required()).
		String("description", "skill 的可复用能力简述。", tool.Required()).
		String("risk", "风险等级：safe, low, medium, high, critical。", tool.Required()).
		String("elyph", "完整 SKILL.elyph 内容"+elyph.RuleCard(), tool.Required()).
		String("go_source", "可选，Go main 包源码；提供时写入 main.go 并编译 binary。源码必须从 os.Stdin 读取业务参数 JSON；不要依赖 os.Args 传业务参数。不提供时创建纯 ELyph 文本 skill。").
		Integer("timeout_ms", "可选，编译超时时间，默认 60000。").
		BuildSchema()
}

func (t CreateElSkillTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args createElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse create_el_skill arguments: %w", err)
		}
	}
	if t.Manager == nil || t.Manager.Registry == nil {
		return nil, fmt.Errorf("skill manager is not configured")
	}
	name := strings.TrimSpace(args.Name)
	if err := validateSkillName(name); err != nil {
		return nil, err
	}
	if args.Description = strings.TrimSpace(args.Description); args.Description == "" {
		return nil, fmt.Errorf("description is required")
	}
	if _, err := parseRisk(args.Risk); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.GoSource) != "" {
		if err := validateGoSource(args.GoSource); err != nil {
			return nil, err
		}
	}
	if _, err := elyph.ParseSkill(args.Elyph, name); err != nil {
		return nil, err
	}
	args.Elyph = withElyphMetadata(args.Elyph, args.Description, args.Risk)
	if _, exists := t.Manager.Registry.Get(name); exists {
		return nil, fmt.Errorf("tool or skill %q already exists", name)
	}
	root := filepath.Join(t.Manager.Root, "go", name)
	if _, err := os.Stat(root); err == nil {
		return nil, fmt.Errorf("skill directory %q already exists", root)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat skill directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create skill directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, elyph.SkillFileName), []byte(args.Elyph), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", elyph.SkillFileName, err)
	}
	if strings.TrimSpace(args.GoSource) != "" {
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(args.GoSource), 0o644); err != nil {
			return nil, fmt.Errorf("write main.go: %w", err)
		}
		binary := name
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		timeout := 60 * time.Second
		if args.TimeoutMS > 0 {
			timeout = time.Duration(args.TimeoutMS) * time.Millisecond
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(runCtx, "go", "build", "-o", binary, ".")
		cmd.Dir = root
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("go build failed: %w\nstdout:\n%s\nstderr:\n%s", err, truncateOutput(stdout.String()), truncateOutput(stderr.String()))
		}
	}
	if err := t.Manager.Reload(ctx); err != nil {
		return nil, fmt.Errorf("ELyph skill written but reload failed: %w", err)
	}
	return &tool.Result{Content: fmt.Sprintf("created ELyph skill %s", name)}, nil
}

func validateSkillName(name string) error {
	if !skillNamePattern.MatchString(name) {
		return fmt.Errorf("invalid skill name %q", name)
	}
	if isWindowsReservedName(name) {
		return fmt.Errorf("invalid skill name %q: reserved file name", name)
	}
	return nil
}

func isWindowsReservedName(name string) bool {
	base := strings.ToUpper(strings.TrimSpace(name))
	reserved := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}
	return reserved[base]
}

func validateGoSource(source string) error {
	if !strings.Contains(source, "package main") {
		return fmt.Errorf("go_source must contain package main")
	}
	return nil
}

func withElyphMetadata(text, description, risk string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return text
	}
	header := strings.Fields(lines[0])
	if len(header) >= 2 {
		lines[0] = header[0] + " " + header[1]
		if description = strings.TrimSpace(description); description != "" {
			lines[0] += " - " + description
		}
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[0])
	if risk = strings.TrimSpace(risk); risk != "" {
		out = append(out, "** risk "+risk)
	}
	out = append(out, lines[1:]...)
	return strings.Join(out, "\n")
}

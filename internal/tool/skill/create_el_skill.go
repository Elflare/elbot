// 实现原生 Skill 创建工具，组织技能文档、Go 源码与初始编译。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"elbot/internal/elyph"
	"elbot/internal/fileops"
	"elbot/internal/llm"
)

const CreateElSkillName = "create_el_skill"

var skillNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type CreateElSkillTool struct {
	manager *Manager
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
	return CreateElSkillTool{manager: manager}
}

func (CreateElSkillTool) Name() string { return CreateElSkillName }

func (CreateElSkillTool) Info() toolshare.Info {
	return toolshare.NewBuilder(CreateElSkillName).
		NeedsElyphRules().
		Description("创建 ElBot 原生 ELyph skill。只有可复用经验、固定流程或稳定能力才应创建 skill；一次性任务不要创建。可只写 SKILL.elyph，也可附带 Go 源码并通过 go_skill_run 调用。").
		Source(toolshare.SourceBuiltin).
		Risk(toolshare.RiskHigh).
		DependsOn(ReadElSkillName, ModifyElSkillName, FinalizeElSkillName).
		BuildInfo()
}

func (CreateElSkillTool) Schema() llm.ToolSchema {
	return toolshare.NewBuilder(CreateElSkillName).
		Description("创建 ElBot 原生 ELyph Skill，可附带 go_source 并编译。Go Skill 通过 go_skill_run 调用，宿主处理 payload 后将其作为 JSON 写入进程 stdin。调用方用 payload.media_inputs 传入 [{\"media\":\"media:<sha256>\"}]；宿主补充 path、name、mime_type、size、可选 base64 和 media_workspace。无媒体输入但需要输出目录时传空 media_inputs。stdout 可返回 content 和 segments；image/file 使用稳定 media ID 或 Skill 目录内的相对 path，宿主导入 path 后返回稳定 ID。调用结束自动清理临时目录和引用。SKILL.elyph 必须写明 go_skill_run 的调用方式和业务 payload。").
		String("name", "skill 名称，也是目录名和 binary 名；使用小写字母、数字、下划线或短横线。", toolshare.Required()).
		String("description", "skill 的可复用能力简述。", toolshare.Required()).
		String("risk", "风险等级：safe, low, medium, high, critical。", toolshare.Required()).
		String("elyph", "完整 SKILL.elyph 内容。", toolshare.Required()).
		String(
			"go_source",
			"可选的 Go main 包源码。程序从 os.Stdin 读取 payload JSON，不使用 os.Args 或 ELBOT_MEDIA_N；媒体从 media_inputs.path 读取，输出写入 media_workspace，并通过 stdout segments 返回相对 path。不提供时创建纯 ELyph 文本 Skill。",
		).
		Integer("timeout_ms", "可选，编译超时时间，默认 60000。").
		BuildSchema()
}

func (t CreateElSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args createElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse create_el_skill arguments: %w", err)
				return nil, callErr
			}
		}
	}
	name := strings.TrimSpace(args.Name)
	if err := validateSkillName(name); err != nil {
		return nil, err
	}
	if args.Description = strings.TrimSpace(args.Description); args.Description == "" {
		{
			callErr := fmt.Errorf("description is required")
			return nil, callErr
		}
	}
	if _, err := parseRisk(args.Risk); err != nil {
		return nil, err
	}
	goVersion := ""
	if strings.TrimSpace(args.GoSource) != "" {
		if err := validateGoSource(args.GoSource); err != nil {
			return nil, err
		}
		var err error
		goVersion, err = detectGoModVersion(ctx, t.manager.processEnv)
		if err != nil {
			return nil, err
		}
	}
	_, diagnostics, err := elyph.ValidateSkill(args.Elyph, name)
	if err != nil {
		return nil, err
	}
	args.Elyph = withElyphMetadata(args.Elyph, args.Description, args.Risk)
	if _, exists := t.manager.registry.Get(name); exists {
		{
			callErr := fmt.Errorf("tool or skill %q already exists", name)
			return nil, callErr
		}
	}
	root := filepath.Join(t.manager.root, "go", name)
	if _, err := os.Stat(root); err == nil {
		{
			callErr := fmt.Errorf("skill directory %q already exists", root)
			return nil, callErr
		}
	} else if err != nil && !os.IsNotExist(err) {
		{
			callErr := fmt.Errorf("stat skill directory: %w", err)
			return nil, callErr
		}
	}
	files := make([]*fileops.PreparedFile, 0, 3)
	contents := []struct{ path, content string }{{filepath.Join(root, elyph.SkillFileName), args.Elyph}}
	if strings.TrimSpace(args.GoSource) != "" {
		contents = append(contents, struct{ path, content string }{filepath.Join(root, "main.go"), args.GoSource}, struct{ path, content string }{filepath.Join(root, "go.mod"), goModContent(name, goVersion)})
	}
	var preview strings.Builder
	fmt.Fprintf(&preview, "创建 Skill：%s\n目录：%s\n", name, root)
	for _, content := range contents {
		file, err := fileops.PrepareFileWrite(ctx, content.path, content.content)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
		preview.WriteString(file.Preview().Diff)
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, preview.String(), func(ctx context.Context) (_ *toolshare.Result, logContent string, _ error) {
		defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
		t.manager.writeMu.Lock()
		defer t.manager.writeMu.Unlock()
		if _, exists := t.manager.registry.Get(name); exists {
			err := fmt.Errorf("tool or skill %q already exists", name)
			return nil, err.Error(), err
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			if err == nil {
				err = fmt.Errorf("skill directory %q appeared after preparation", root)
			}
			return nil, err.Error(), err
		}
		for _, file := range files {
			if _, err := file.Commit(ctx); err != nil {
				return nil, err.Error(), err
			}
		}
		if strings.TrimSpace(args.GoSource) != "" {
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
			cmd := t.manager.processEnv.CommandContext(runCtx, "go", "build", "-o", binary, ".")
			cmd.Dir = root
			stdout := limitedOutput{limit: maxRunnerStdout}
			stderr := limitedOutput{limit: maxRunnerOutput}
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				{
					callErr := fmt.Errorf("go build failed: %w\nstdout:\n%s\nstderr:\n%s", err, truncateOutput(stdout.String()), truncateOutput(stderr.String()))
					return nil, callErr.Error(), callErr
				}
			}
		}
		err := t.manager.reloadLocked(ctx)
		t.manager.setReloadResult(err)
		if err != nil {
			callErr := fmt.Errorf("ELyph skill written but reload failed: %w", err)
			return nil, callErr.Error(), callErr
		}
		result := &toolshare.Result{Content: appendElyphWarnings(fmt.Sprintf("created ELyph skill %s", name), diagnostics)}
		return result, result.Content, nil
	}), nil
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

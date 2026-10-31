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
		Description("创建可复用EL Skill，一次性任务不创建；有go_source→编译并用go_skill_run调用，否则→纯ELyph文档。").
		Source(toolshare.SourceBuiltin).
		Risk(toolshare.RiskHigh).
		DependsOn(ReadElSkillName, ModifyElSkillName, FinalizeElSkillName).
		BuildInfo()
}

func (t CreateElSkillTool) Schema() llm.ToolSchema {
	schema := toolshare.NewBuilder(CreateElSkillName).
		Description(t.Info().Description).
		String("name", "Skill名称。", toolshare.Required()).
		String("description", "可复用能力简述。", toolshare.Required()).
		String("risk", "执行风险。", toolshare.Required(), toolshare.Enum("safe", "low", "medium", "high", "critical")).
		String("elyph", "完整SKILL.elyph；有go_source⇒写明go_skill_run用法和业务payload。", toolshare.Required()).
		String(
			"go_source",
			`Go package main；stdin=payload JSON，非argv/ELBOT_MEDIA_N。
payload.media_inputs=[{media:"media:<sha256>"}]→每项补path,name,mime_type,size,base64?；[]→仅申请media_workspace。
读media_inputs[*].path，输出写media_workspace；路径均相对Skill目录。
stdout={content?,segments?}；text segment={type:"text",text}；image/file segment={type:"image"或"file",media或path}，path由宿主导入为media ID。`,
		).
		Integer("timeout_ms", "编译超时，ms；≤0/省略→60000。").
		BuildSchema()
	schema.Parameters["properties"].(map[string]any)["name"].(map[string]any)["pattern"] = skillNamePattern.String()
	return schema
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
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, preview.String(), func(ctx context.Context) (*toolshare.Result, string, error) {
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

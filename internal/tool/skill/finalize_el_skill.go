// 实现 Skill 完成工具，校验并完成 Skill 创建或修改。
package skill

import toolshare "elbot/internal/tool/share"

import (
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

const FinalizeElSkillName = "finalize_el_skill"

type FinalizeElSkillTool struct {
	Manager *Manager
}

type finalizeElSkillArgs struct {
	Name      string `json:"name"`
	TimeoutMS int    `json:"timeout_ms"`
}

func NewFinalizeElSkillTool(manager *Manager) FinalizeElSkillTool {
	return FinalizeElSkillTool{Manager: manager}
}

func (FinalizeElSkillTool) Name() string { return FinalizeElSkillName }

func (FinalizeElSkillTool) Info() toolshare.Info {
	return toolshare.NewBuilder(FinalizeElSkillName).
		Description("完成 EL Skill 修改：校验 SKILL.elyph；如有 main.go，则 gofmt、校验 package main、go build；成功后 reload。错误会返回给 LLM 继续修。").
		Source(toolshare.SourceBuiltin).
		Risk(toolshare.RiskMedium).
		BuildInfo()
}

func (FinalizeElSkillTool) Schema() llm.ToolSchema {
	return toolshare.NewBuilder(FinalizeElSkillName).
		Description("完成 EL Skill 修改并检查结果。会校验 SKILL.elyph；如果存在 main.go，会自动 gofmt 写回、校验 package main、go build；成功后 reload。gofmt/build/语法错误会作为结果内容返回，供继续修改。").
		String("name", "skill 名称。", toolshare.Required()).
		Integer("timeout_ms", "可选，Go 编译超时时间，默认 60000。").
		BuildSchema()
}

func (t FinalizeElSkillTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	var args finalizeElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse finalize_el_skill arguments: %w", err)
				return nil, callErr.Error(), callErr
			}
		}
	}
	name := strings.TrimSpace(args.Name)
	if err := validateSkillName(name); err != nil {
		return nil, err.Error(), err
	}
	root := filepath.Join(t.Manager.Root, "go", name)
	elyphPath := filepath.Join(root, elyph.SkillFileName)
	elyphData, err := os.ReadFile(elyphPath)
	if err != nil {
		{
			callErr := fmt.Errorf("read %s: %w", elyph.SkillFileName, err)
			return nil, callErr.Error(), callErr
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "finalize EL skill %s\n\n", name)
	_, diagnostics, err := elyph.ValidateSkill(string(elyphData), name)
	if err != nil {
		fmt.Fprintf(&b, "ELyph: failed\n%s\n", err)
		{
			result := &toolshare.Result{Content: b.String()}
			return result, fmt.Sprintf("ELyph: failed\n%s", err), nil
		}
	}
	b.WriteString("ELyph: ok\n")
	if warnings := elyph.WarningDiagnostics(diagnostics); len(warnings) > 0 {
		b.WriteString("ELyph warnings:\n")
		b.WriteString(elyph.FormatDiagnostics(warnings))
		b.WriteByte('\n')
	}

	sourcePath := filepath.Join(root, "main.go")
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		b.WriteString("Go source: not found, text-only skill\n")
		if err := t.Manager.Reload(ctx); err != nil {
			{
				callErr := fmt.Errorf("EL skill finalized but reload failed: %w", err)
				return nil, callErr.Error(), callErr
			}
		}
		b.WriteString("reload: ok\n")
		{
			result := &toolshare.Result{Content: b.String()}
			return result, "text-only skill; reload: ok\n" + result.Content, nil
		}
	} else if err != nil {
		{
			callErr := fmt.Errorf("stat main.go: %w", err)
			return nil, callErr.Error(), callErr
		}
	}
	file, err := fileops.ReadFile(sourcePath, "")
	if err != nil {
		return nil, err.Error(), err
	}
	b.WriteString("Go source: found\n")

	formatted, err := formatGoSource(file.Text)
	if err != nil {
		fmt.Fprintf(&b, "gofmt: failed\n%s\n", err)
		{
			result := &toolshare.Result{Content: b.String()}
			return result, fmt.Sprintf("gofmt: failed\n%s", err), nil
		}
	}
	if formatted != file.Text {
		if _, err := fileops.WriteTextFile(sourcePath, file, formatted); err != nil {
			return nil, err.Error(), err
		}
		b.WriteString("gofmt: changed\n")
	} else {
		b.WriteString("gofmt: unchanged\n")
	}
	if err := validateGoMainSource(formatted); err != nil {
		fmt.Fprintf(&b, "Go source validation: failed\n%s\n", err)
		{
			result := &toolshare.Result{Content: b.String()}
			return result, fmt.Sprintf("Go source validation: failed\n%s", err), nil
		}
	}
	b.WriteString("Go source validation: ok\n")

	build := goBuildResult{}
	build, err = runGoBuild(ctx, root, name, args.TimeoutMS, t.Manager.ProcessEnv)
	if err != nil {
		return nil, err.Error(), err
	}
	if build.Err != nil {
		fmt.Fprintf(&b, "go build: failed\n%v\n", build.Err)
		appendCommandOutput(&b, build.Stdout, build.Stderr)
		{
			result := &toolshare.Result{Content: b.String()}
			return result, fmt.Sprintf("go build: failed\n%v\nstderr:\n%s\nstdout:\n%s", build.Err, build.Stderr, build.Stdout), nil
		}
	}
	b.WriteString("go build: ok\n")
	appendCommandOutput(&b, build.Stdout, build.Stderr)

	if err := t.Manager.Reload(ctx); err != nil {
		{
			callErr := fmt.Errorf("EL skill finalized but reload failed: %w", err)
			return nil, callErr.Error(), callErr
		}
	}
	b.WriteString("reload: ok\n")
	{
		result := &toolshare.Result{Content: b.String()}
		return result, "go build: ok; reload: ok\n" + result.Content, nil
	}
}

func appendCommandOutput(b *strings.Builder, stdout, stderr string) {
	if strings.TrimSpace(stdout) != "" {
		fmt.Fprintf(b, "stdout:\n%s\n", truncateOutput(stdout))
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprintf(b, "stderr:\n%s\n", truncateOutput(stderr))
	}
}

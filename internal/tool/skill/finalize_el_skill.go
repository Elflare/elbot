// 实现 Skill 完成工具，校验并完成 Skill 创建或修改。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/fileops"
)

const FinalizeElSkillName = "finalize_el_skill"

type FinalizeElSkillTool struct {
	manager *Manager
}

type finalizeElSkillArgs struct {
	Name      string `json:"name"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (t FinalizeElSkillTool) Definition() toolshare.ToolDefinition {
	builder := toolshare.NewBuilder(FinalizeElSkillName).
		Description("校验SKILL.elyph；有main.go→gofmt+校验package main+build；成功→reload。").
		Risk(toolshare.RiskMedium)

	return builder.
		String("name", "Skill名称。", toolshare.Required()).
		Integer("timeout_ms", "编译超时，ms；≤0/省略→60000。").Build(t.PrepareCall)
}

func (t FinalizeElSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (*toolshare.PreparedCall, error) {
	var args finalizeElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse finalize_el_skill arguments: %w", err)
		}
	}
	name := strings.TrimSpace(args.Name)
	if err := validateSkillName(name); err != nil {
		return nil, err
	}
	root := filepath.Join(t.manager.root, "go", name)
	elyphPath := filepath.Join(root, elyph.SkillFileName)
	elyphData, err := os.ReadFile(elyphPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", elyph.SkillFileName, err)
	}
	elyphTarget, err := fileops.ResolveFileTarget(elyphPath, false)
	if err != nil {
		return nil, err
	}
	elyphRevision := fileops.ContentRevision(elyphData)
	var report strings.Builder
	fmt.Fprintf(&report, "finalize EL skill %s\n\n", name)
	_, diagnostics, validationErr := elyph.ValidateSkill(string(elyphData), name)
	if validationErr != nil {
		fmt.Fprintf(&report, "ELyph: failed\n%s\n", validationErr)
	} else {
		report.WriteString("ELyph: ok\n")
		if warnings := elyph.WarningDiagnostics(diagnostics); len(warnings) > 0 {
			report.WriteString("ELyph warnings:\n" + elyph.FormatDiagnostics(warnings) + "\n")
		}
	}
	sourcePath := filepath.Join(root, "main.go")
	var source fileops.File
	var sourceTarget string
	var formatted string
	var prepared *fileops.PreparedFile
	textOnly := false
	if validationErr == nil {
		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			textOnly = true
			report.WriteString("Go source: not found, text-only skill\n")
		} else if err != nil {
			return nil, fmt.Errorf("stat main.go: %w", err)
		} else {
			source, err = fileops.ReadFile(sourcePath, "")
			if err != nil {
				return nil, err
			}
			sourceTarget, err = fileops.ResolveFileTarget(sourcePath, false)
			if err != nil {
				return nil, err
			}
			report.WriteString("Go source: found\n")
			formatted, validationErr = formatGoSource(source.Text)
			if validationErr != nil {
				fmt.Fprintf(&report, "gofmt: failed\n%s\n", validationErr)
			} else {
				if formatted != source.Text {
					prepared, err = fileops.PrepareFileEdit(ctx, fileops.EditRequest{
						Path: sourcePath, Encoding: source.Encoding, ExpectedRevision: fileops.ContentRevision(source.Bytes),
						Edits: []fileops.Edit{{Operation: "overwrite", NewText: &formatted}},
					}, nil)
					if err != nil {
						return nil, err
					}
					report.WriteString("gofmt: changed\n")
				} else {
					report.WriteString("gofmt: unchanged\n")
				}
				validationErr = validateGoMainSource(formatted)
				if validationErr != nil {
					fmt.Fprintf(&report, "Go source validation: failed\n%s\n", validationErr)
				} else {
					report.WriteString("Go source validation: ok\n")
				}
			}
		}
	}
	previewHeader := fmt.Sprintf("Finalize Skill：%s\n目录：%s\n校验、格式化、编译并重载", name, root)
	details := []toolshare.ConfirmationDetailBlock{{Kind: toolshare.ConfirmationDetailText, Content: previewHeader}}
	if prepared != nil {
		filePreview := prepared.Preview()
		details = []toolshare.ConfirmationDetailBlock{toolshare.FileDiffConfirmationDetail(
			filePreview.Diff,
			filePreview.DiffAvailable,
			previewHeader+"\ndiff:",
		)}
	}
	content := report.String()
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (*toolshare.Result, string, error) {
		t.manager.writeMu.Lock()
		defer t.manager.writeMu.Unlock()
		checkSource := func(path, target, revision string) error {
			currentTarget, err := fileops.ResolveFileTarget(path, false)
			if err != nil {
				return err
			}
			if currentTarget != target {
				return fmt.Errorf("skill target changed after preparation")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if fileops.ContentRevision(data) != revision {
				return fmt.Errorf("skill source changed after preparation")
			}
			return nil
		}
		if err := checkSource(elyphPath, elyphTarget, elyphRevision); err != nil {
			return nil, err.Error(), err
		}
		if prepared != nil {
			if _, err := prepared.Commit(ctx); err != nil {
				return nil, err.Error(), err
			}
		} else if source.Path != "" {
			if err := checkSource(source.Path, sourceTarget, fileops.ContentRevision(source.Bytes)); err != nil {
				return nil, err.Error(), err
			}
		}
		if validationErr != nil {
			return &toolshare.Result{Content: content}, fmt.Sprintf("skill=%s validation=failed build=skipped reload=skipped", name), nil
		}
		var b strings.Builder
		b.WriteString(content)
		summary := fmt.Sprintf("skill=%s validation=ok build=skipped", name)
		if textOnly {
			if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
				if err == nil {
					err = fmt.Errorf("skill source appeared after preparation")
				}
				return nil, err.Error(), err
			}
		} else {
			build, err := runGoBuild(ctx, root, name, args.TimeoutMS, t.manager.processEnv)
			if err != nil {
				return nil, fmt.Sprintf("skill=%s validation=ok build=failed reload=skipped", name), err
			}
			if build.Err != nil {
				fmt.Fprintf(&b, "go build: failed\n%v\n", build.Err)
				appendCommandOutput(&b, build.Stdout, build.Stderr)
				summary := fmt.Sprintf("skill=%s validation=ok build=failed", name)
				var exitErr *exec.ExitError
				if errors.As(build.Err, &exitErr) && exitErr.ExitCode() >= 0 {
					summary += fmt.Sprintf(" exit_code=%d", exitErr.ExitCode())
				}
				return &toolshare.Result{Content: b.String()}, summary + " reload=skipped", nil
			}
			b.WriteString("go build: ok\n")
			appendCommandOutput(&b, build.Stdout, build.Stderr)
			summary = fmt.Sprintf("skill=%s validation=ok build=ok exit_code=0", name)
		}
		err := t.manager.reloadLocked(ctx)
		t.manager.setReloadResult(err)
		if err != nil {
			callErr := fmt.Errorf("EL skill finalized but reload failed: %w", err)
			return nil, summary + " reload=failed", callErr
		}
		b.WriteString("reload: ok\n")
		return &toolshare.Result{Content: b.String()}, summary + " reload=ok", nil
	}), nil
}

func appendCommandOutput(b *strings.Builder, stdout, stderr string) {
	if strings.TrimSpace(stdout) != "" {
		fmt.Fprintf(b, "stdout:\n%s\n", truncateOutput(stdout))
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprintf(b, "stderr:\n%s\n", truncateOutput(stderr))
	}
}

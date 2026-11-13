// 实现 Skill 完成工具，校验并完成 Skill 创建或修改。
package skill

import (
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

func (t FinalizeElSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args finalizeElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse finalize_el_skill arguments: %v", err)}, nil
		}
	}
	name := strings.TrimSpace(args.Name)
	if err := validateSkillName(name); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	root := filepath.Join(t.manager.root, "go", name)
	elyphPath := filepath.Join(root, elyph.SkillFileName)
	elyphData, err := os.ReadFile(elyphPath)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.NotFound, Message: fmt.Sprintf("EL skill %q not found", name)}, nil
	}
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, fmt.Errorf("read %s: %w", elyph.SkillFileName, err)
	}
	elyphTarget, err := fileops.ResolveFileTarget(elyphPath, false)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	if elyphTarget.Status != operationresult.Success {
		return operationresult.Result[*toolshare.PreparedCall]{Status: elyphTarget.Status, Message: elyphTarget.Message}, nil
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
			return operationresult.Result[*toolshare.PreparedCall]{}, fmt.Errorf("stat main.go: %w", err)
		} else {
			loaded, err := fileops.ReadFile(sourcePath, "")
			if err != nil {
				return operationresult.Result[*toolshare.PreparedCall]{}, err
			}
			if loaded.Status != operationresult.Success {
				return operationresult.Result[*toolshare.PreparedCall]{Status: loaded.Status, Message: loaded.Message}, nil
			}
			source = loaded.Data
			selected, err := fileops.ResolveFileTarget(sourcePath, false)
			if err != nil {
				return operationresult.Result[*toolshare.PreparedCall]{}, err
			}
			if selected.Status != operationresult.Success {
				return operationresult.Result[*toolshare.PreparedCall]{Status: selected.Status, Message: selected.Message}, nil
			}
			sourceTarget = selected.Data
			report.WriteString("Go source: found\n")
			formatted, validationErr = formatGoSource(source.Text)
			if validationErr != nil {
				fmt.Fprintf(&report, "gofmt: failed\n%s\n", validationErr)
			} else {
				if formatted != source.Text {
					preflight, err := fileops.PrepareFileEdit(ctx, fileops.EditRequest{
						Path: sourcePath, Encoding: source.Encoding, ExpectedRevision: fileops.ContentRevision(source.Bytes),
						Edits: []fileops.Edit{{Operation: "overwrite", NewText: &formatted}},
					}, nil)
					if err != nil {
						return operationresult.Result[*toolshare.PreparedCall]{}, err
					}
					if preflight.Status != operationresult.Success {
						return operationresult.Result[*toolshare.PreparedCall]{Status: preflight.Status, Message: preflight.Message}, nil
					}
					prepared = preflight.Data
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
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		t.manager.writeMu.Lock()
		defer t.manager.writeMu.Unlock()
		checkSource := func(path, target, revision string) (operationresult.Result[struct{}], error) {
			currentTarget, err := fileops.ResolveFileTarget(path, false)
			if err != nil {
				return operationresult.Result[struct{}]{}, err
			}
			if currentTarget.Status != operationresult.Success {
				return operationresult.Result[struct{}]{Status: currentTarget.Status, Message: currentTarget.Message}, nil
			}
			if currentTarget.Data != target {
				return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "skill target changed after preparation"}, nil
			}
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: fmt.Sprintf("file %q not found", path)}, nil
			}
			if err != nil {
				return operationresult.Result[struct{}]{}, err
			}
			if fileops.ContentRevision(data) != revision {
				return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "skill source changed after preparation"}, nil
			}
			return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
		}
		checked, err := checkSource(elyphPath, elyphTarget.Data, elyphRevision)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: checked.Status, Message: checked.Message}, checked.Message, nil
		}
		if prepared != nil {
			committed, err := prepared.Commit(ctx)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if committed.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: committed.Status, Message: committed.Message}, "skill finalize rejected: " + committed.Message, nil
			}
		} else if source.Path != "" {
			checked, err := checkSource(source.Path, sourceTarget, fileops.ContentRevision(source.Bytes))
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if checked.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: checked.Status, Message: checked.Message}, checked.Message, nil
			}
		}
		if validationErr != nil {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: content}, fmt.Sprintf("skill=%s validation=failed build=skipped reload=skipped", name), nil
		}
		var b strings.Builder
		b.WriteString(content)
		summary := fmt.Sprintf("skill=%s validation=ok build=skipped", name)
		if textOnly {
			if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
				if err == nil {
					return operationresult.Result[*toolshare.Result]{Status: operationresult.Conflict, Message: "skill source appeared after preparation"}, "skill source appeared after preparation", nil
				}
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
		} else {
			build, err := runGoBuild(ctx, root, name, args.TimeoutMS, t.manager.processEnv)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, fmt.Sprintf("skill=%s validation=ok build=failed reload=skipped", name), err
			}
			if build.Status != operationresult.Success {
				fmt.Fprintf(&b, "go build: %s\n%s\n", build.Status, build.Message)
				return operationresult.Result[*toolshare.Result]{Status: build.Status, Message: b.String()}, fmt.Sprintf("skill=%s validation=ok build=%s reload=skipped", name, build.Status), nil
			}
			b.WriteString("go build: ok\n")
			appendCommandOutput(&b, build.Data.Stdout, build.Data.Stderr)
			summary = fmt.Sprintf("skill=%s validation=ok build=ok exit_code=0", name)
		}
		err = t.manager.reloadLocked(ctx)
		t.manager.setReloadResult(err)
		if err != nil {
			callErr := fmt.Errorf("EL skill finalized but reload failed: %w", err)
			return operationresult.Result[*toolshare.Result]{}, summary + " reload=failed", callErr
		}
		b.WriteString("reload: ok\n")
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: b.String()}}, summary + " reload=ok", nil
	})}, nil
}

func appendCommandOutput(b *strings.Builder, stdout, stderr string) {
	if strings.TrimSpace(stdout) != "" {
		fmt.Fprintf(b, "stdout:\n%s\n", truncateOutput(stdout))
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprintf(b, "stderr:\n%s\n", truncateOutput(stderr))
	}
}

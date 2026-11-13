// 实现原生 Skill 创建工具，组织技能文档、Go 源码与初始编译。
package skill

import (
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
)

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/fileops"
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

func (t CreateElSkillTool) Definition() toolshare.ToolDefinition {
	builder := toolshare.NewBuilder(CreateElSkillName).
		NeedsElyphRules().
		Description("创建可复用EL Skill，一次性任务不创建；有go_source→编译并用go_skill_run调用，否则→纯ELyph文档。").
		Risk(toolshare.RiskHigh).
		DependsOn(ReadElSkillName, ModifyElSkillName, FinalizeElSkillName)

	schema := builder.
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
	definition := builder.Build(t.PrepareCall)
	definition.Parameters = schema.Parameters

	return definition
}

func (t CreateElSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args createElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse create_el_skill arguments: %v", err)}, nil
		}
	}
	name := strings.TrimSpace(args.Name)
	if err := validateSkillName(name); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	if args.Description = strings.TrimSpace(args.Description); args.Description == "" {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "description is required"}, nil
	}
	if _, err := parseRisk(args.Risk); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	goVersion := ""
	if strings.TrimSpace(args.GoSource) != "" {
		if err := validateGoSource(args.GoSource); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
		}
		version, err := detectGoModVersion(ctx, t.manager.processEnv)
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{}, err
		}
		if version.Status != operationresult.Success {
			return operationresult.Result[*toolshare.PreparedCall]{Status: version.Status, Message: version.Message}, nil
		}
		goVersion = version.Data
	}
	_, diagnostics, err := elyph.ValidateSkill(args.Elyph, name)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	args.Elyph = withElyphMetadata(args.Elyph, args.Description, args.Risk)
	if _, exists := t.manager.registry.Get(name); exists {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Conflict, Message: fmt.Sprintf("tool or skill %q already exists", name)}, nil
	}
	root := filepath.Join(t.manager.root, "go", name)
	if _, err := os.Stat(root); err == nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Conflict, Message: fmt.Sprintf("skill directory %q already exists", root)}, nil
	} else if err != nil && !os.IsNotExist(err) {
		{
			callErr := fmt.Errorf("stat skill directory: %w", err)
			return operationresult.Result[*toolshare.PreparedCall]{}, callErr
		}
	}
	files := make([]*fileops.PreparedFile, 0, 3)
	contents := []struct{ path, content string }{{filepath.Join(root, elyph.SkillFileName), args.Elyph}}
	if strings.TrimSpace(args.GoSource) != "" {
		contents = append(contents, struct{ path, content string }{filepath.Join(root, "main.go"), args.GoSource}, struct{ path, content string }{filepath.Join(root, "go.mod"), goModContent(name, goVersion)})
	}
	header := fmt.Sprintf("创建 Skill：%s\n目录：%s", name, root)
	var details []toolshare.ConfirmationDetailBlock
	for _, content := range contents {
		file, err := fileops.PrepareFileWrite(ctx, content.path, content.content)
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{}, err
		}
		if file.Status != operationresult.Success {
			return operationresult.Result[*toolshare.PreparedCall]{Status: file.Status, Message: file.Message}, nil
		}
		files = append(files, file.Data)
		filePreview := file.Data.Preview()
		details = append(details, toolshare.FileDiffConfirmationDetail(
			filePreview.Diff,
			filePreview.DiffAvailable,
			fmt.Sprintf("%s\n文件：%s\n%s", header, content.path, filePreview.Diff),
		))
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		t.manager.writeMu.Lock()
		defer t.manager.writeMu.Unlock()
		if _, exists := t.manager.registry.Get(name); exists {
			message := fmt.Sprintf("tool or skill %q already exists", name)
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Conflict, Message: message}, message, nil
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			if err == nil {
				return operationresult.Result[*toolshare.Result]{Status: operationresult.Conflict, Message: fmt.Sprintf("skill directory %q appeared after preparation", root)}, "skill directory appeared after preparation", nil
			}
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		for _, file := range files {
			committed, err := file.Commit(ctx)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if committed.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: committed.Status, Message: committed.Message}, "skill file write rejected: " + committed.Message, nil
			}
		}
		summary := fmt.Sprintf("skill=%s build=skipped", name)
		if strings.TrimSpace(args.GoSource) != "" {
			build, err := runGoBuild(ctx, root, name, args.TimeoutMS, t.manager.processEnv)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, fmt.Sprintf("skill=%s build=failed", name), err
			}
			if build.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: build.Status, Message: build.Message}, fmt.Sprintf("skill=%s build=%s", name, build.Status), nil
			}
			summary = fmt.Sprintf("skill=%s build=ok exit_code=0", name)
		}
		err := t.manager.reloadLocked(ctx)
		t.manager.setReloadResult(err)
		if err != nil {
			callErr := fmt.Errorf("ELyph skill written but reload failed: %w", err)
			return operationresult.Result[*toolshare.Result]{}, summary + " reload=failed", callErr
		}
		result := &toolshare.Result{Content: appendElyphWarnings(fmt.Sprintf("created ELyph skill %s", name), diagnostics)}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, summary + " reload=ok", nil
	})}, nil
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

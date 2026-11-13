// 将文档型 Skill 适配为可发现的工具入口。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
)

type AgentSkillTool struct {
	manager *Manager
}

type agentSkillArgs struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Toml   string `json:"toml"`
}

func (t AgentSkillTool) Definition() toolshare.ToolDefinition {
	builder := toolshare.NewBuilder(AgentSkillManagerName).
		Description(agentSkillToolDescription()).
		Risk(toolshare.RiskHigh).
		Hidden()

	definition := builder.Build(t.PrepareCall)
	definition.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"read", "write"}},
			"name":   map[string]any{"type": "string", "description": "AgentSkill名称。"},
			"toml":   map[string]any{"type": "string", "description": "write必填，完整" + AgentSkillConfigFile + "。"},
		},
		"required": []string{"action", "name"},
	}
	return definition
}

func agentSkillToolDescription() string {
	return "读或写 AgentSkill 的 ELBOT_SKILL.toml。"
}

func (t AgentSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	prepared, err := t.preview(ctx, req)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*toolshare.PreparedCall]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	preview := prepared.Data
	details := []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(fmt.Sprintf("读取 AgentSkill %s 的 %s", preview.Name, AgentSkillConfigFile))}
	if preview.Action == "write" {
		filePreview := preview.File.Preview()
		details = []toolshare.ConfirmationDetailBlock{toolshare.FileDiffConfirmationDetail(
			filePreview.Diff,
			filePreview.DiffAvailable,
			fmt.Sprintf("写入 AgentSkill %s 的 %s\n文件：%s\ndiff:", preview.Name, AgentSkillConfigFile, preview.Path),
		)}
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		return t.call(ctx, preview)
	})}, nil
}

func (t AgentSkillTool) call(ctx context.Context, preview agentSkillPreview) (operationresult.Result[*toolshare.Result], string, error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*toolshare.Result]{}, err.Error(), err
	}
	if preview.Action == "read" {
		record, ok := t.manager.catalog.Get(preview.Name)
		if !ok || record.Kind != KindAgent || AgentSkillConfigPath(record.Root) != preview.Path {
			message := "AgentSkill target changed after preflight"
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Conflict, Message: message}, message, nil
		}
		content, err := readAgentSkillConfig(record, preview.Path)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		result := &toolshare.Result{Content: content}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("action=read skill=%s path=%s", preview.Name, preview.Path), nil
	}
	committed, err := t.manager.commitAgentSkillConfig(ctx, preview.Name, preview.Path, preview.File)
	if err != nil {
		callErr := fmt.Errorf("write %s for AgentSkill %s: %w", AgentSkillConfigFile, preview.Name, err)
		return operationresult.Result[*toolshare.Result]{}, callErr.Error(), callErr
	}
	if committed.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: committed.Status, Message: committed.Message}, committed.Message, nil
	}
	result := &toolshare.Result{Content: fmt.Sprintf("wrote %s for AgentSkill %s and reloaded skills", AgentSkillConfigFile, preview.Name)}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
}

type agentSkillPreview struct {
	Action string
	Name   string
	Path   string
	File   *fileops.PreparedFile // 配置写入的批准内容与文件修订。
}

func (t AgentSkillTool) preview(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[agentSkillPreview], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[agentSkillPreview]{}, err
	}
	var args agentSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[agentSkillPreview]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse agent_skill arguments: %v", err)}, nil
		}
	}
	name := strings.TrimSpace(args.Name)
	if err := validateSkillName(name); err != nil {
		return operationresult.Result[agentSkillPreview]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	record, ok := t.manager.catalog.Get(name)
	if !ok || record.Kind != KindAgent {
		return operationresult.Result[agentSkillPreview]{Status: operationresult.NotFound, Message: fmt.Sprintf("AgentSkill %q not found", name)}, nil
	}
	path := AgentSkillConfigPath(record.Root)
	action := strings.TrimSpace(args.Action)
	switch action {
	case "read":
		return operationresult.Result[agentSkillPreview]{Status: operationresult.Success, Data: agentSkillPreview{Action: action, Name: name, Path: path}}, nil
	case "write":
		if strings.TrimSpace(args.Toml) == "" {
			return operationresult.Result[agentSkillPreview]{Status: operationresult.InvalidArguments, Message: "toml is required for write"}, nil
		}
		if _, err := ParseAgentSkillManifest([]byte(args.Toml)); err != nil {
			return operationresult.Result[agentSkillPreview]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
		}
		file, err := fileops.PrepareFileWrite(ctx, path, args.Toml)
		if err != nil {
			return operationresult.Result[agentSkillPreview]{}, err
		}
		if file.Status != operationresult.Success {
			return operationresult.Result[agentSkillPreview]{Status: file.Status, Message: file.Message}, nil
		}
		return operationresult.Result[agentSkillPreview]{Status: operationresult.Success, Data: agentSkillPreview{Action: action, Name: name, Path: path, File: file.Data}}, nil
	default:
		return operationresult.Result[agentSkillPreview]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("invalid action %q; use read or write", args.Action)}, nil
	}
}

func readAgentSkillConfig(record record, path string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "AgentSkill: %s\n", record.Name)
	fmt.Fprintf(&b, "Config file: %s\n", AgentSkillConfigFile)
	if record.Manifest.Callable {
		b.WriteString("Status: callable\n")
	} else if record.ManifestFound {
		b.WriteString("Status: document-only\nPolicy: configured\n")
	} else {
		b.WriteString("Status: document-only\n")
	}
	if data, err := os.ReadFile(path); err == nil {
		b.WriteString("\nCurrent TOML:\n")
		b.Write(data)
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteByte('\n')
		}
	} else if os.IsNotExist(err) {
		b.WriteString("\nNo current TOML.\n")
	} else {
		return "", err
	}
	b.WriteString("\nFormat:\n")
	b.WriteString("# 文档型 Skill 只限制可见性时，可以只写：\nrisk = \"high\"\nsuperadmin_only = true\n\n# 注册成普通工具时，补齐命令和 schema：\nrisk = \"medium\"\nsuperadmin_only = false\ntags = [\"doc\"]\ncommand = [\"python\", \"foo.py\"]\ntimeout_seconds = 30\nexpose_root = false\n\nparameters = '''\n{\n  \"type\": \"object\",\n  \"required\": [\"input\"],\n  \"properties\": {\n    \"input\": {\"type\": \"string\", \"description\": \"输入文本\"},\n    \"mode\": {\"type\": \"string\", \"description\": \"处理模式\"}\n  }\n}\n'''\n\n[args]\ninput = \"--input\"\nmode = \"--mode\"\n")
	return strings.TrimRight(b.String(), "\n"), nil
}

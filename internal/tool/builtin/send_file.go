package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/output"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

type SendFileTool struct {
	artifacts *ArtifactManager
}

type sendFileArgs struct {
	Path     string `json:"path"`
	File     string `json:"file"`
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
}

func NewSendFileTool(artifacts *ArtifactManager) SendFileTool {
	return SendFileTool{artifacts: artifacts}
}

func (t SendFileTool) Name() string { return "send_file" }

func (t SendFileTool) Info() tool.Info { return sendFileBuilder().BuildInfo() }

func (t SendFileTool) Schema() llm.ToolSchema { return sendFileBuilder().BuildSchema() }

func sendFileBuilder() *tool.Builder {
	return tool.NewBuilder("send_file").
		Description("发送本地文件。").
		Risk(tool.RiskMedium).
		SuperadminOnly().
		String("path", "要发送的文件路径。可用相对路径；path 和 file 至少填一个。").
		String("file", "path 的别名，便于直接指定文件。path 和 file 至少填一个。").
		String("name", "可选，发送时展示的文件名。").
		String("mime_type", "可选，文件 MIME 类型；不填时按扩展名推断。")
}

func (t SendFileTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	args, err := parseSendFileArgs(req)
	if err != nil {
		return tool.RiskAssessment{}, err
	}
	path := args.sourcePath()
	if strings.TrimSpace(path) == "" {
		return tool.RiskAssessment{}, fmt.Errorf("path or file is required")
	}
	sandbox, hasSandbox := tool.SandboxContextFromContext(ctx)
	if t.isExternalPath(sandbox, hasSandbox, path) {
		if hasSandbox && sandbox.BackgroundKind == tool.BackgroundKindCron {
			return tool.RiskAssessment{Level: tool.RiskMedium, Reasons: []string{"cron 后台发送外部文件会自动复制到 artifact 后发送"}}, nil
		}
		return tool.RiskAssessment{Level: tool.RiskHigh, Reasons: []string{"发送外部文件需要确认，确认后会复制到 artifact 再发送"}}, nil
	}
	return tool.RiskAssessment{Level: tool.RiskMedium}, nil
}

func (t SendFileTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	args, err := parseSendFileArgs(req)
	if err != nil {
		return nil, err
	}
	sandbox, _ := tool.SandboxContextFromContext(ctx)
	prepared, err := t.artifacts.Prepare(sandbox, args.sourcePath(), args.Name, args.MIMEType)
	if err != nil {
		return nil, err
	}
	out := output.FilePath(prepared.Path)
	out.Name = prepared.Name
	out.Source.MIMEType = prepared.MIMEType
	if sandbox.BackgroundKind == tool.BackgroundKindCron {
		if msg, ok := platform.MessageContextFrom(ctx); ok && strings.TrimSpace(msg.Platform) != "" {
			out.Target = output.Target{Platform: msg.Platform, Superadmins: true}
		}
	}
	return &tool.Result{Content: fmt.Sprintf("已发送文件：%s", prepared.Name), Outputs: []output.Output{out}}, nil
}

func (t SendFileTool) isExternalPath(sandbox tool.SandboxContext, hasSandbox bool, path string) bool {
	path = normalizeLocalPath(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) && hasSandbox && strings.TrimSpace(sandbox.Dir) != "" {
		return false
	}
	resolved := path
	if !filepath.IsAbs(resolved) {
		abs, err := filepath.Abs(resolved)
		if err == nil {
			resolved = abs
		}
	}
	if t.artifacts != nil {
		if isPathWithin(resolved, t.artifacts.ArtifactDir) || isPathWithin(resolved, t.artifacts.SandboxRoot) {
			return false
		}
	}
	return true
}

func (args sendFileArgs) sourcePath() string {
	if strings.TrimSpace(args.Path) != "" {
		return args.Path
	}
	return args.File
}

func parseSendFileArgs(req tool.CallRequest) (sendFileArgs, error) {
	var args sendFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return args, fmt.Errorf("parse send_file arguments: %w", err)
		}
	}
	return args, nil
}

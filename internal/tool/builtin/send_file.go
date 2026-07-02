package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

type SendFileTool struct {
	files *FileManager
}

type sendFileArgs struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
}

func NewSendFileTool(files *FileManager) SendFileTool {
	return SendFileTool{files: files}
}

func (t SendFileTool) Name() string { return "send_file" }

func (t SendFileTool) Info() tool.Info { return sendFileBuilder().BuildInfo() }

func (t SendFileTool) Schema() llm.ToolSchema { return sendFileBuilder().BuildSchema() }

func sendFileBuilder() *tool.Builder {
	return tool.NewBuilder("send_file").
		Description("发送本地文件。").
		Risk(tool.RiskMedium).
		SuperadminOnly().
		String("path", "要发送的文件路径。可用相对路径。").
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
		return tool.RiskAssessment{}, fmt.Errorf("path is required")
	}
	resolved, err := tool.ResolveWorkspacePath(ctx, path, tool.PathResolveOptions{})
	if err != nil {
		return tool.RiskAssessment{}, err
	}
	if resolved.WasAbs {
		return tool.RiskAssessment{Level: tool.RiskHigh, Reasons: []string{"发送外部文件需要确认"}}, nil
	}
	return tool.RiskAssessment{Level: tool.RiskMedium}, nil
}

func (t SendFileTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	args, err := parseSendFileArgs(req)
	if err != nil {
		return nil, err
	}
	sandbox, _ := tool.SandboxContextFromContext(ctx)
	resolved, err := tool.ResolveWorkspacePath(ctx, args.sourcePath(), tool.PathResolveOptions{})
	if err != nil {
		return nil, err
	}
	prepared, err := t.files.Prepare(resolved.Path, args.Name, args.MIMEType)
	if err != nil {
		return nil, err
	}
	out := delivery.FilePath(prepared.Path)
	out.Name = prepared.Name
	out.Source.MIMEType = prepared.MIMEType
	if sandbox.BackgroundKind == tool.BackgroundKindCron {
		if msg, ok := platform.MessageContextFrom(ctx); ok && strings.TrimSpace(msg.Platform) != "" {
			out.Target = delivery.Target{Platform: msg.Platform, Superadmins: true}
		}
	}
	return &tool.Result{Content: fmt.Sprintf("已发送文件：%s", prepared.Name), Warnings: resolved.Warnings, Outputs: []delivery.Output{out}}, nil
}

func (args sendFileArgs) sourcePath() string {
	return args.Path
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

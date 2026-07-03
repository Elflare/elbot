package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	urlpath "path"
	"path/filepath"
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
	Source   string `json:"source"`
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
		Description("发送文件。").
		Risk(tool.RiskMedium).
		SuperadminOnly().
		String("source", "要发送的文件来源。可以是本地路径、file:// URI 或 HTTP(S) URL；本地路径不需要加 file://。", tool.Required()).
		String("name", "可选，发送时展示的文件名。").
		String("mime_type", "可选，文件 MIME 类型；不填时按扩展名推断。")
}

func (t SendFileTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	args, err := parseSendFileArgs(req)
	if err != nil {
		return tool.RiskAssessment{}, err
	}
	source := args.source()
	if source == "" {
		return tool.RiskAssessment{}, fmt.Errorf("source is required")
	}
	if delivery.IsHTTPMediaSource(source) {
		return tool.RiskAssessment{Level: tool.RiskMedium}, nil
	}
	localSource, err := localSourcePath(source)
	if err != nil {
		return tool.RiskAssessment{}, err
	}
	resolved, err := tool.ResolveWorkspacePath(ctx, localSource, tool.PathResolveOptions{})
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
	source := args.source()
	if source == "" {
		return nil, fmt.Errorf("source is required")
	}
	sandbox, _ := tool.SandboxContextFromContext(ctx)
	out, warnings, label, err := t.buildOutput(ctx, args, source)
	if err != nil {
		return nil, err
	}
	if sandbox.BackgroundKind == tool.BackgroundKindCron {
		if msg, ok := platform.MessageContextFrom(ctx); ok && strings.TrimSpace(msg.Platform) != "" {
			out.Target = delivery.Target{Platform: msg.Platform, Superadmins: true}
		}
	}
	return &tool.Result{Content: fmt.Sprintf("已发送文件：%s", label), Warnings: warnings, Outputs: []delivery.Output{out}}, nil
}

func (t SendFileTool) buildOutput(ctx context.Context, args sendFileArgs, source string) (delivery.Output, []string, string, error) {
	if delivery.IsHTTPMediaSource(source) {
		urlName := fileNameFromURL(source)
		name := safeFileName(firstNonEmptyString(args.Name, urlName))
		mimeType := firstNonEmptyString(args.MIMEType, mimeTypeFromName(urlName), mimeTypeFromName(name))
		out := outputForMediaType(mimeType, "")
		out.Source.URL = source
		out.Source.MIMEType = mimeType
		out.Name = name
		return out, nil, name, nil
	}
	localSource, err := localSourcePath(source)
	if err != nil {
		return delivery.Output{}, nil, "", err
	}
	resolved, err := tool.ResolveWorkspacePath(ctx, localSource, tool.PathResolveOptions{})
	if err != nil {
		return delivery.Output{}, nil, "", err
	}
	prepared, err := t.files.Prepare(resolved.Path, args.Name, args.MIMEType)
	if err != nil {
		return delivery.Output{}, nil, "", err
	}
	out := outputForMediaType(prepared.MIMEType, prepared.Path)
	out.Name = prepared.Name
	out.Source.MIMEType = prepared.MIMEType
	return out, resolved.Warnings, prepared.Name, nil
}

func outputForMediaType(mimeType, path string) delivery.Output {
	if isImageMIME(mimeType) {
		return delivery.ImagePath(path)
	}
	return delivery.FilePath(path)
}

func isImageMIME(mimeType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/")
}

func mimeTypeFromName(name string) string {
	return mime.TypeByExtension(filepath.Ext(strings.TrimSpace(name)))
}

func fileNameFromURL(value string) string {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return urlpath.Base(u.Path)
}

func localSourcePath(source string) (string, error) {
	if !delivery.IsFileMediaSource(source) {
		return source, nil
	}
	return delivery.FileURIToPath(source)
}

func (args sendFileArgs) source() string {
	return strings.TrimSpace(args.Source)
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

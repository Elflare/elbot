// 实现文件发送工具，解析目标文件并组织发送请求。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	urlpath "path"
	"path/filepath"
	"runtime"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	toolmedia "elbot/internal/tool/media"
	"elbot/internal/workspace"
)

type SendFileTool struct {
	media *toolmedia.MediaRuntime
}

type sendFileArgs struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
}

func NewSendFileTool(media *toolmedia.MediaRuntime) SendFileTool {
	return SendFileTool{media: media}
}

func (t SendFileTool) Name() string { return "send_file" }

func (t SendFileTool) Info() toolshare.Info { return sendFileBuilder().BuildInfo() }

func (t SendFileTool) Schema() llm.ToolSchema { return sendFileBuilder().BuildSchema() }

func sendFileBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("send_file").
		Description("发送文件。").
		Risk(toolshare.RiskMedium).
		SuperadminOnly().
		String("source", "要发送的文件来源。支持本地路径、file URI、HTTP(S) URL 或 Media Center 媒体 ID。").
		String("name", "可选，发送时展示的文件名。").
		String("mime_type", "可选，文件 MIME 类型；不填时按扩展名推断。")
}

func (t SendFileTool) AssessRisk(ctx context.Context, req toolshare.CallRequest) (toolshare.RiskAssessment, error) {
	args, err := parseSendFileArgs(req)
	if err != nil {
		return toolshare.RiskAssessment{}, err
	}
	source := args.source()
	if source == "" {
		return toolshare.RiskAssessment{}, fmt.Errorf("source is required")
	}
	if strings.HasPrefix(source, toolmedia.IDPrefix) {
		if !toolmedia.ValidID(source) {
			return toolshare.RiskAssessment{}, fmt.Errorf("invalid media ID")
		}
		return toolshare.RiskAssessment{Level: toolshare.RiskMedium}, nil
	}
	if delivery.IsHTTPMediaSource(source) {
		return toolshare.RiskAssessment{Level: toolshare.RiskMedium}, nil
	}
	localSource, err := localSourcePath(source)
	if err != nil {
		return toolshare.RiskAssessment{}, err
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, localSource, workspace.PathResolveOptions{RequireRegularFile: true})
	if err != nil {
		return toolshare.RiskAssessment{}, err
	}
	if resolved.WasAbs {
		return toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"发送外部文件需要确认"}}, nil
	}
	return toolshare.RiskAssessment{Level: toolshare.RiskMedium}, nil
}

func (t SendFileTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	args, err := parseSendFileArgs(req)
	if err != nil {
		return nil, err.Error(), err
	}
	source := args.source()
	if source == "" {
		{
			callErr := fmt.Errorf("source is required")
			return nil, callErr.Error(), callErr
		}
	}
	backgroundKind, _ := contextinfo.BackgroundKindFromContext(ctx)
	out, warnings, label, err := t.buildOutput(ctx, args, source)
	if err != nil {
		return nil, err.Error(), err
	}
	if backgroundKind == contextinfo.BackgroundKindCron {
		if info, ok := contextinfo.ConversationFromContext(ctx); ok && strings.TrimSpace(info.Source.Platform) != "" {
			out.Target = delivery.Target{Platform: info.Source.Platform, Superadmins: true}
		}
	}
	{
		result := &toolshare.Result{Content: fmt.Sprintf("已发送文件：%s", label), Warnings: warnings, Outputs: []delivery.Output{out}}
		return result, fmt.Sprintf("file output prepared: %s", label), nil
	}
}

func (t SendFileTool) buildOutput(ctx context.Context, args sendFileArgs, source string) (delivery.Output, []string, string, error) {
	if strings.HasPrefix(source, toolmedia.IDPrefix) {
		if !toolmedia.ValidID(source) {
			return delivery.Output{}, nil, "", fmt.Errorf("invalid media ID")
		}
		item, err := t.media.Metadata(ctx, source)
		if err != nil {
			return delivery.Output{}, nil, "", err
		}
		name := safeFileName(firstNonEmptyString(args.Name, item.Name))
		out := outputForMediaType(item.MIMEType, "")
		out.Name = name
		out.Source = delivery.Source{MediaID: item.ID, MIMEType: item.MIMEType}
		return out, nil, name, nil
	}
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
	resolved, err := workspace.ResolveWorkspacePath(ctx, localSource, workspace.PathResolveOptions{RequireRegularFile: true})
	if err != nil {
		return delivery.Output{}, nil, "", err
	}
	name := safeFileName(firstNonEmptyString(args.Name, filepath.Base(resolved.Path)))
	mimeType := firstNonEmptyString(args.MIMEType, mime.TypeByExtension(filepath.Ext(name)))
	out := outputForMediaType(mimeType, resolved.Path)
	out.Name = name
	out.Source.MIMEType = mimeType
	var warnings []string
	if resolved.WasAbs && !contextinfo.BackgroundContext(ctx) {
		warnings = append(warnings, toolshare.AbsolutePathWorkspaceWarning)
	}
	return out, warnings, name, nil
}

func safeFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "file"
	}
	return name
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
	source = strings.TrimSpace(source)
	if !strings.HasPrefix(strings.ToLower(source), "file://") {
		return source, nil
	}
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid file URI")
	}
	path := u.Path
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	if path == "" {
		return "", fmt.Errorf("invalid file URI")
	}
	return filepath.FromSlash(path), nil
}

func (args sendFileArgs) source() string {
	return strings.TrimSpace(args.Source)
}

func parseSendFileArgs(req toolshare.CallRequest) (sendFileArgs, error) {
	var args sendFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return args, fmt.Errorf("parse send_file arguments: %w", err)
		}
	}
	return args, nil
}

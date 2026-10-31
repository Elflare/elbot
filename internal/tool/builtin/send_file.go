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
	"elbot/internal/media"
	"elbot/internal/workspace"
)

type SendFileTool struct {
	media interface {
		Metadata(context.Context, string) (toolshare.MediaInfo, error)
	}
}

type sendFileArgs struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
}

func NewSendFileTool(media interface {
	Metadata(context.Context, string) (toolshare.MediaInfo, error)
}) SendFileTool {
	return SendFileTool{media: media}
}

func (t SendFileTool) Name() string { return "send_file" }

func (t SendFileTool) Info() toolshare.Info { return sendFileBuilder().BuildInfo() }

func (t SendFileTool) Schema() llm.ToolSchema { return sendFileBuilder().BuildSchema() }

func sendFileBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("send_file").
		Description("发送文件到当前会话对应目标。").
		Risk(toolshare.RiskLow).
		String("source", "media ID / HTTP(S) / 本地路径 / file URI。", toolshare.Required()).
		String("name", "展示文件名。").
		String("mime_type", "MIME类型；省略时按扩展名推断。")
}

func (t SendFileTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	args, err := parseSendFileArgs(req)
	if err != nil {
		return nil, err
	}
	source := args.source()
	if source == "" {
		return nil, fmt.Errorf("source is required")
	}
	assessment := toolshare.RiskAssessment{Level: t.Info().Risk}
	var resolved workspace.ResolvedPath
	if strings.HasPrefix(source, media.IDPrefix) {
		if !media.ValidID(source) {
			return nil, fmt.Errorf("invalid media ID")
		}
	} else if !delivery.IsHTTPMediaSource(source) {
		actor, ok := contextinfo.ActorFromContext(ctx)
		if !ok || actor.Role != contextinfo.RoleSuperadmin {
			return nil, fmt.Errorf("发送本地文件需要超级管理员权限")
		}
		localSource, err := localSourcePath(source)
		if err != nil {
			return nil, err
		}
		resolved, err = workspace.ResolveWorkspacePath(ctx, localSource, workspace.PathResolveOptions{RequireRegularFile: true})
		if err != nil {
			return nil, err
		}
		assessment = toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"发送本地文件"}}
		source = resolved.Path
	}
	return toolshare.NewPreparedCall(req, assessment, fmt.Sprintf("发送文件：%s", source), func(ctx context.Context) (*toolshare.Result, string, error) {
		backgroundKind, _ := contextinfo.BackgroundKindFromContext(ctx)
		out, warnings, label, err := t.buildOutput(ctx, args, source, resolved)
		if err != nil {
			return nil, err.Error(), err
		}
		if backgroundKind == contextinfo.BackgroundKindCron {
			if info, ok := contextinfo.ConversationFromContext(ctx); ok && strings.TrimSpace(info.Source.Platform) != "" {
				out.Target = delivery.Target{Platform: info.Source.Platform, Superadmins: true}
			}
		}
		result := &toolshare.Result{Content: fmt.Sprintf("已发送文件：%s", label), Warnings: warnings, Outputs: []delivery.Output{out}}
		return result, fmt.Sprintf("file output prepared: %s", label), nil
	}), nil
}

func (t SendFileTool) buildOutput(ctx context.Context, args sendFileArgs, source string, resolved workspace.ResolvedPath) (delivery.Output, []string, string, error) {
	if strings.HasPrefix(source, media.IDPrefix) {
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

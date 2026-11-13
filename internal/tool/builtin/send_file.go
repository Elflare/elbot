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
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/workspace"
)

type SendFileTool struct {
	media interface {
		Metadata(context.Context, string) (operationresult.Result[toolshare.MediaInfo], error)
	}
}

type sendFileArgs struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
}

func (t SendFileTool) Definition() toolshare.ToolDefinition {
	return sendFileBuilder().Build(t.PrepareCall)
}

func sendFileBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("send_file").
		Description("发送文件到当前会话对应目标。").
		Risk(toolshare.RiskLow).
		String("source", "media ID / HTTP(S) / 本地路径 / file URI。", toolshare.Required()).
		String("name", "展示文件名。").
		String("mime_type", "MIME类型；省略时按扩展名推断。")
}

func (t SendFileTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	args, err := parseSendFileArgs(req)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	source := args.source()
	if source == "" {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "source is required"}, nil
	}
	assessment := toolshare.RiskAssessment{Level: info.Risk}
	var resolved workspace.ResolvedPath
	if strings.HasPrefix(source, media.IDPrefix) {
		if !media.ValidID(source) {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "invalid media ID"}, nil
		}
	} else if !delivery.IsHTTPMediaSource(source) {
		actor, ok := contextinfo.ActorFromContext(ctx)
		if !ok || actor.Role != contextinfo.RoleSuperadmin {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Forbidden, Message: "发送本地文件需要超级管理员权限"}, nil
		}
		localSource, err := localSourcePath(source)
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidTarget, Message: err.Error()}, nil
		}
		selected, err := workspace.ResolveWorkspacePath(ctx, localSource, workspace.PathResolveOptions{RequireRegularFile: true})
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{}, err
		}
		if selected.Status != operationresult.Success {
			return operationresult.Result[*toolshare.PreparedCall]{Status: selected.Status, Message: selected.Message}, nil
		}
		resolved = selected.Data
		assessment = toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"发送本地文件"}}
		source = resolved.Path
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(fmt.Sprintf("发送文件：%s", source))}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		out, warnings, label, err := t.buildOutput(ctx, args, source, resolved)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if out.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: out.Status, Message: out.Message}, out.Message, nil
		}
		out.Data.Target = toolshare.PolicyFromContext(ctx).OutputTarget
		result := &toolshare.Result{Content: fmt.Sprintf("已发送文件：%s", label), Warnings: warnings, Outputs: []delivery.Output{out.Data}}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("file output prepared: %s", label), nil
	})}, nil
}

func (t SendFileTool) buildOutput(ctx context.Context, args sendFileArgs, source string, resolved workspace.ResolvedPath) (operationresult.Result[delivery.Output], []string, string, error) {
	if strings.HasPrefix(source, media.IDPrefix) {
		item, err := t.media.Metadata(ctx, source)
		if err != nil {
			return operationresult.Result[delivery.Output]{}, nil, "", err
		}
		if item.Status != operationresult.Success {
			return operationresult.Result[delivery.Output]{Status: item.Status, Message: item.Message}, nil, "", nil
		}
		name := safeFileName(firstNonEmptyString(args.Name, item.Data.Name))
		out := outputForMediaType(item.Data.MIMEType, "")
		out.Name = name
		out.Source = delivery.Source{MediaID: item.Data.ID, MIMEType: item.Data.MIMEType}
		return operationresult.Result[delivery.Output]{Status: operationresult.Success, Data: out}, nil, name, nil
	}
	if delivery.IsHTTPMediaSource(source) {
		urlName := fileNameFromURL(source)
		name := safeFileName(firstNonEmptyString(args.Name, urlName))
		mimeType := firstNonEmptyString(args.MIMEType, mimeTypeFromName(urlName), mimeTypeFromName(name))
		out := outputForMediaType(mimeType, "")
		out.Source.URL = source
		out.Source.MIMEType = mimeType
		out.Name = name
		return operationresult.Result[delivery.Output]{Status: operationresult.Success, Data: out}, nil, name, nil
	}
	name := safeFileName(firstNonEmptyString(args.Name, filepath.Base(resolved.Path)))
	mimeType := firstNonEmptyString(args.MIMEType, mime.TypeByExtension(filepath.Ext(name)))
	out := outputForMediaType(mimeType, resolved.Path)
	out.Name = name
	out.Source.MIMEType = mimeType
	var warnings []string
	if resolved.WasAbs {
		warnings = append(warnings, toolshare.AbsolutePathWorkspaceWarning)
	}
	return operationresult.Result[delivery.Output]{Status: operationresult.Success, Data: out}, warnings, name, nil
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

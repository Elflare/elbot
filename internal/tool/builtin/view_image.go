// 实现图片查看工具，组织模型可用的图片结果。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/tool/chat_history"
	toolmedia "elbot/internal/tool/media"
	"elbot/internal/workspace"
)

type ViewImageTool struct {
	center  *toolmedia.MediaRuntime
	history *chat_history.Manager
}

type viewImageArgs struct {
	Source     string   `json:"source"`
	MessageIDs []string `json:"message_id"`
	MediaIndex [][]int  `json:"media_index"`
}

func NewViewImageTool(center *toolmedia.MediaRuntime, history *chat_history.Manager) ViewImageTool {
	return ViewImageTool{center: center, history: history}
}

func (ViewImageTool) Name() string { return "view_image" }
func viewImageBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("view_image").
		Description("查看图片，已经看到的图片勿再次使用。source 与 message_id 二选一。").
		APITypes(llm.APITypeResponse).Risk(toolshare.RiskLow).Tags("chat", "files").
		String("source", "媒体 ID、HTTP(S) URL 或本地路径。").
		StringArray("message_id", "当前聊天的消息 ID 数组，可带 #。")
}
func (ViewImageTool) Info() toolshare.Info { return viewImageBuilder().BuildInfo() }
func (ViewImageTool) Schema() llm.ToolSchema {
	schema := viewImageBuilder().BuildSchema()
	properties := schema.Parameters["properties"].(map[string]any)
	properties["message_id"].(map[string]any)["minItems"] = 1
	properties["media_index"] = map[string]any{
		"type": "array", "minItems": 1,
		"description": "与 message_id 逐项对应的媒体序号数组，从 1 开始，只能选图片；省略时每条消息取首张图片。",
		"items":       map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 1}},
	}
	return schema
}

func parseViewImageArgs(req toolshare.CallRequest) (viewImageArgs, error) {
	var args viewImageArgs
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return args, fmt.Errorf("parse view_image arguments: %w", err)
	}
	args.Source = strings.TrimSpace(args.Source)
	if (args.Source != "") == (len(args.MessageIDs) > 0) {
		return args, fmt.Errorf("source 与 message_id 必须二选一")
	}
	if args.Source != "" {
		if args.MediaIndex != nil || args.MessageIDs != nil {
			return args, fmt.Errorf("source 不能与消息参数同时使用")
		}
		return args, nil
	}
	if args.MediaIndex != nil && len(args.MediaIndex) != len(args.MessageIDs) {
		return args, fmt.Errorf("media_index 必须与 message_id 逐项对应")
	}
	for i, id := range args.MessageIDs {
		args.MessageIDs[i] = parseChatHistoryMessageID(id)
		if args.MessageIDs[i] == "" {
			return args, fmt.Errorf("message_id 不能为空")
		}
		if args.MediaIndex != nil {
			if len(args.MediaIndex[i]) == 0 {
				return args, fmt.Errorf("media_index 每项不能为空")
			}
			for _, index := range args.MediaIndex[i] {
				if index < 1 {
					return args, fmt.Errorf("媒体序号从 1 开始")
				}
			}
		}
	}
	return args, nil
}

// sourcePath checks local access before touching the filesystem. Call repeats
// this check so invoking the handler cannot bypass argument-specific permission.
func (t ViewImageTool) sourcePath(ctx context.Context, source string) (workspace.ResolvedPath, error) {
	if source == "" || delivery.IsHTTPMediaSource(source) {
		return workspace.ResolvedPath{}, nil
	}
	if strings.HasPrefix(source, toolmedia.IDPrefix) {
		if !toolmedia.ValidID(source) {
			return workspace.ResolvedPath{}, fmt.Errorf("invalid media ID")
		}
		return workspace.ResolvedPath{}, nil
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return workspace.ResolvedPath{}, fmt.Errorf("查看本地图片需要超级管理员权限")
	}
	path, err := localSourcePath(source)
	if err != nil {
		return workspace.ResolvedPath{}, err
	}
	return workspace.ResolveWorkspacePath(ctx, path, workspace.PathResolveOptions{})
}

func (t ViewImageTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	args, err := parseViewImageArgs(req)
	if err != nil {
		return nil, err
	}
	path, err := t.sourcePath(ctx, args.Source)
	if err != nil {
		return nil, err
	}
	assessment := toolshare.RiskAssessment{Level: t.Info().Risk}
	if path.Path != "" && isSensitiveReadFile(path.Path) {
		assessment = toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"读取可能包含凭据的敏感文件，需要用户确认"}}
	}
	preview := fmt.Sprintf("查看图片：%s\n消息：%v", args.Source, args.MessageIDs)
	if path.Path != "" {
		preview = fmt.Sprintf("查看图片：%s", path.Path)
	}
	return toolshare.NewPreparedCall(req, assessment, preview, func(ctx context.Context) (_ *toolshare.Result, logContent string, _ error) {
		defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
		if args.Source == "" {
			return t.viewMessages(ctx, args)
		}
		var err error
		var item toolshare.MediaInfo
		if strings.HasPrefix(args.Source, toolmedia.IDPrefix) {
			item, err = t.center.ValidateImage(ctx, args.Source)
		} else {
			if delivery.IsHTTPMediaSource(args.Source) {
				item, err = t.center.ImportURL(ctx, args.Source, toolshare.MediaImportOptions{})
			} else {
				item, err = t.center.ImportFile(ctx, path.Path, toolshare.MediaImportOptions{})
			}
			if err != nil {
				return nil, err.Error(), err
			}
			item, err = t.center.ValidateImage(ctx, item.ID)
		}
		if err != nil {
			return nil, err.Error(), err
		}
		{
			var warnings []string
			if path.WasAbs && !contextinfo.BackgroundContext(ctx) {
				warnings = append(warnings, toolshare.AbsolutePathWorkspaceWarning)
			}
			result := &toolshare.Result{Segments: []llm.MessageSegment{imageResultSegment(item)}, Warnings: warnings}
			return result, fmt.Sprintf("image prepared media_id=%s source=%s", item.ID, args.Source), nil
		}
	}), nil
}

func (t ViewImageTool) viewMessages(ctx context.Context, args viewImageArgs) (_ *toolshare.Result, logContent string, _ error) {
	results, err := t.history.GetImages(ctx, chat_history.MediaRequest{MessageIDs: args.MessageIDs, MediaIndex: args.MediaIndex})
	if err != nil {
		return nil, err.Error(), err
	}
	var segments []llm.MessageSegment
	for _, result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err.Error(), err
		}
		label := fmt.Sprintf("[#%s] %d.", result.MessageID, result.Index)
		switch {
		case errors.Is(result.Err, chat_history.ErrFetchLimit):
			label += fmt.Sprintf(" 达到本次 %d 个下载尝试上限", chat_history.MaxFetchAttempts)
		case result.Err != nil:
			label += " 图片获取失败"
			logContent += fmt.Sprintf("\n%s %v", label, result.Err)
		default:
			item, err := t.center.ValidateImage(ctx, result.Media.ID)
			if err != nil {
				label += " 媒体不是有效或支持的图片"
				logContent += fmt.Sprintf("\n%s %v", label, err)
			} else {
				segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: label}, imageResultSegment(item))
				continue
			}
		}
		segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: label})
	}
	return &toolshare.Result{Segments: segments}, fmt.Sprintf("view_image message_id=%v media_index=%v", args.MessageIDs, args.MediaIndex) + logContent, ctx.Err()
}

func imageResultSegment(item toolshare.MediaInfo) llm.MessageSegment {
	return llm.MessageSegment{Type: llm.SegmentImage, MediaID: item.ID, Name: item.Name, MIMEType: item.MIMEType}
}

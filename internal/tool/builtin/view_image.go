package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/workspace"
)

type ViewImageTool struct {
	center  *media.Manager
	history storage.ChatHistoryRepository
}

type viewImageArgs struct {
	Source     string   `json:"source"`
	MessageIDs []string `json:"message_id"`
	MediaIndex [][]int  `json:"media_index"`
}

func NewViewImageTool(center *media.Manager, history storage.ChatHistoryRepository) ViewImageTool {
	return ViewImageTool{center: center, history: history}
}

func (ViewImageTool) Name() string { return "view_image" }
func viewImageBuilder() *tool.Builder {
	return tool.NewBuilder("view_image").
		Description("查看图片，已经看到的图片勿再次使用。source 与 message_id 二选一。").
		APITypes(llm.APITypeResponse).Risk(tool.RiskLow).Tags("chat", "files").
		String("source", "媒体 ID、HTTP(S) URL 或本地路径。").
		StringArray("message_id", "当前聊天的消息 ID 数组，可带 #。")
}
func (ViewImageTool) Info() tool.Info { return viewImageBuilder().BuildInfo() }
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

func parseViewImageArgs(req tool.CallRequest) (viewImageArgs, error) {
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
	if strings.HasPrefix(source, media.IDPrefix) {
		if !media.ValidID(source) {
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

func (t ViewImageTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	args, err := parseViewImageArgs(req)
	if err != nil {
		return tool.RiskAssessment{}, err
	}
	path, err := t.sourcePath(ctx, args.Source)
	if err != nil {
		return tool.RiskAssessment{}, err
	}
	if path.Path != "" && isSensitiveReadFile(path.Path) {
		return tool.RiskAssessment{Level: tool.RiskHigh, Reasons: []string{"读取可能包含凭据的敏感文件，需要用户确认"}}, nil
	}
	return tool.RiskAssessment{Level: tool.RiskLow}, nil
}

func (t ViewImageTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	_, err := t.AssessRisk(ctx, req)
	return err
}

func (t ViewImageTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	args, err := parseViewImageArgs(req)
	if err != nil {
		return nil, err
	}
	if t.center == nil {
		return nil, fmt.Errorf("media center is not configured")
	}
	if args.Source == "" {
		return t.viewMessages(ctx, args)
	}
	path, err := t.sourcePath(ctx, args.Source)
	if err != nil {
		return nil, err
	}
	var item *storage.Media
	switch {
	case strings.HasPrefix(args.Source, media.IDPrefix):
		item, err = t.center.Metadata(ctx, args.Source)
	case delivery.IsHTTPMediaSource(args.Source):
		item, err = t.center.ImportURL(ctx, args.Source, media.Input{})
	default:
		item, err = t.center.ImportFile(ctx, path.Path, media.Input{})
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("图片获取失败：来源不可用或超限")
	}
	item, err = t.center.ImageMetadata(ctx, item.ID)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("媒体不是有效或支持的图片")
	}
	return &tool.Result{Segments: []llm.MessageSegment{imageResultSegment(item)}, Warnings: path.Warnings}, nil
}

func (t ViewImageTool) viewMessages(ctx context.Context, args viewImageArgs) (*tool.Result, error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return nil, err
	}
	if t.history == nil {
		return nil, fmt.Errorf("chat history storage is not configured")
	}
	var requests []media.HistoryMediaRequest
	for i, id := range args.MessageIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := t.history.GetByPlatformMessage(ctx, chat.Platform, chat.ScopeID, id)
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("[#%s] 当前聊天没有该消息", id)
		}
		if err != nil {
			return nil, err
		}
		segments := media.HistorySegments(*row)
		var images []int
		for j, segment := range segments {
			if segment.Type == platform.SegmentImage {
				images = append(images, j+1)
			}
		}
		if len(images) == 0 {
			return nil, fmt.Errorf("[#%s] 没有图片", id)
		}
		indexes := images[:1]
		if args.MediaIndex != nil {
			indexes = args.MediaIndex[i]
		}
		for _, index := range indexes {
			if index > len(segments) || segments[index-1].Type != platform.SegmentImage {
				return nil, fmt.Errorf("[#%s] 序号 %d 不是图片；可选图片序号：%v", id, index, images)
			}
			requests = append(requests, media.HistoryMediaRequest{Message: *row, Index: index})
		}
	}
	msg, _ := platform.MessageContextFrom(ctx)
	results, err := t.center.GetHistoryMediaBatch(ctx, requests, msg.MediaResolver, media.HistoryFetchOptions{MaxFetchAttempts: getMediaDownloadLimit})
	if err != nil {
		return nil, err
	}
	var segments []llm.MessageSegment
	for i, result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request := requests[i]
		label := fmt.Sprintf("[#%s] %d.", request.Message.PlatformMessageID, request.Index)
		switch {
		case errors.Is(result.Err, media.ErrHistoryFetchLimit):
			label += " 达到本次 5 个下载尝试上限"
		case result.Err != nil:
			label += " 图片获取失败"
		default:
			item, err := t.center.ImageMetadata(ctx, result.Media.ID)
			if err != nil {
				label += " 媒体不是有效或支持的图片"
			} else {
				segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: label}, imageResultSegment(item))
				continue
			}
		}
		segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: label})
	}
	return &tool.Result{Segments: segments}, ctx.Err()
}

func imageResultSegment(item *storage.Media) llm.MessageSegment {
	return llm.MessageSegment{Type: llm.SegmentImage, MediaID: item.ID, Name: item.Name, MIMEType: item.MIMEType}
}

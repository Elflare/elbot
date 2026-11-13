// 实现图片查看工具，组织模型可用的图片结果。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/workspace"
)

type ViewImageTool struct {
	center interface {
		ValidateImage(context.Context, string) (operationresult.Result[toolshare.MediaInfo], error)
		ImportURL(context.Context, string, toolshare.MediaImportOptions) (operationresult.Result[toolshare.MediaInfo], error)
		ImportFile(context.Context, string, toolshare.MediaImportOptions) (operationresult.Result[toolshare.MediaInfo], error)
	}
	history interface {
		GetImages(context.Context, toolshare.MediaRequest) (operationresult.Result[[]toolshare.MediaResult], error)
	}
}

type viewImageArgs struct {
	Source     string   `json:"source"`
	MessageIDs []string `json:"message_id"`
	MediaIndex [][]int  `json:"media_index"`
}

func (t ViewImageTool) Definition() toolshare.ToolDefinition {
	builder := viewImageBuilder()

	schema := builder.BuildSchema()
	properties := schema.Parameters["properties"].(map[string]any)
	properties["message_id"].(map[string]any)["minItems"] = 1
	properties["media_index"] = map[string]any{
		"type": "array", "minItems": 1,
		"description": "长度同message_id；第i项选消息i的图片媒体编号，沿用历史编号，1-based；省略→每消息首图。",
		"items":       map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 1}},
	}
	definition := builder.Build(t.PrepareCall)
	definition.Parameters = schema.Parameters

	return definition
}
func viewImageBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("view_image").
		Description("看图；source ⊻ message_id；source⇒禁传media_index。当前上下文已可见则无需重复调用。").
		APITypes(llm.APITypeResponse).Risk(toolshare.RiskLow).Tags("chat", "files").
		String("source", "media ID / HTTP(S) / 本地路径 / file URI。").
		StringArray("message_id", "当前聊天的平台消息ID，#可省。")
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
func (t ViewImageTool) sourcePath(ctx context.Context, source string) (operationresult.Result[workspace.ResolvedPath], error) {
	if source == "" || delivery.IsHTTPMediaSource(source) {
		return operationresult.Result[workspace.ResolvedPath]{Status: operationresult.Success}, nil
	}
	if strings.HasPrefix(source, media.IDPrefix) {
		if !media.ValidID(source) {
			return operationresult.Result[workspace.ResolvedPath]{Status: operationresult.InvalidArguments, Message: "invalid media ID"}, nil
		}
		return operationresult.Result[workspace.ResolvedPath]{Status: operationresult.Success}, nil
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[workspace.ResolvedPath]{Status: operationresult.Forbidden, Message: "查看本地图片需要超级管理员权限"}, nil
	}
	path, err := localSourcePath(source)
	if err != nil {
		return operationresult.Result[workspace.ResolvedPath]{Status: operationresult.InvalidTarget, Message: err.Error()}, nil
	}
	return workspace.ResolveWorkspacePath(ctx, path, workspace.PathResolveOptions{})
}

func (t ViewImageTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	args, err := parseViewImageArgs(req)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	path, err := t.sourcePath(ctx, args.Source)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	if path.Status != operationresult.Success {
		return operationresult.Result[*toolshare.PreparedCall]{Status: path.Status, Message: path.Message}, nil
	}
	assessment := toolshare.RiskAssessment{Level: info.Risk}
	if path.Data.Path != "" && isSensitiveReadFile(path.Data.Path) {
		assessment = toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"读取可能包含凭据的敏感文件，需要用户确认"}}
	}
	preview := fmt.Sprintf("查看图片：%s\n消息：%v", args.Source, args.MessageIDs)
	if path.Data.Path != "" {
		preview = fmt.Sprintf("查看图片：%s", path.Data.Path)
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(preview)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		if args.Source == "" {
			return t.viewMessages(ctx, args)
		}
		var err error
		var item operationresult.Result[toolshare.MediaInfo]
		if strings.HasPrefix(args.Source, media.IDPrefix) {
			item, err = t.center.ValidateImage(ctx, args.Source)
		} else {
			if delivery.IsHTTPMediaSource(args.Source) {
				item, err = t.center.ImportURL(ctx, args.Source, toolshare.MediaImportOptions{})
			} else {
				item, err = t.center.ImportFile(ctx, path.Data.Path, toolshare.MediaImportOptions{})
			}
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if item.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: item.Status, Message: item.Message}, item.Message, nil
			}
			item, err = t.center.ValidateImage(ctx, item.Data.ID)
		}
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if item.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: item.Status, Message: item.Message}, item.Message, nil
		}
		var warnings []string
		if path.Data.WasAbs {
			warnings = append(warnings, toolshare.AbsolutePathWorkspaceWarning)
		}
		result := &toolshare.Result{Segments: []llm.MessageSegment{imageResultSegment(item.Data)}, Warnings: warnings}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("image prepared media_id=%s source=%s", item.Data.ID, args.Source), nil
	})}, nil
}

func (t ViewImageTool) viewMessages(ctx context.Context, args viewImageArgs) (operationresult.Result[*toolshare.Result], string, error) {
	results, err := t.history.GetImages(ctx, toolshare.MediaRequest{MessageIDs: args.MessageIDs, MediaIndex: args.MediaIndex})
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, err.Error(), err
	}
	if results.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: results.Status, Message: results.Message}, results.Message, nil
	}
	var segments []llm.MessageSegment
	failed := 0
	for _, result := range results.Data {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		label := fmt.Sprintf("[#%s] %d.", result.MessageID, result.Index)
		if result.Err != nil {
			return operationresult.Result[*toolshare.Result]{}, result.Err.Error(), result.Err
		}
		if result.Media.Status != operationresult.Success {
			label += " " + result.Media.Message
		} else {
			item, err := t.center.ValidateImage(ctx, result.Media.Data.ID)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if item.Status != operationresult.Success {
				label += " " + item.Message
			} else {
				segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: label}, imageResultSegment(item.Data))
				continue
			}
		}
		failed++
		segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: label})
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*toolshare.Result]{}, err.Error(), err
	}
	if failed > 0 && failed == len(results.Data) {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: llm.SegmentsContentText(segments)}, fmt.Sprintf("requested_messages=%d returned_items=%d failed_items=%d", len(args.MessageIDs), len(results.Data), failed), nil
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Segments: segments}}, fmt.Sprintf("requested_messages=%d returned_items=%d failed_items=%d", len(args.MessageIDs), len(results.Data), failed), nil
}

func imageResultSegment(item toolshare.MediaInfo) llm.MessageSegment {
	return llm.MessageSegment{Type: llm.SegmentImage, MediaID: item.ID, Name: item.Name, MIMEType: item.MIMEType}
}

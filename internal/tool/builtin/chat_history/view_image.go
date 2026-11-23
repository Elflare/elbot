package chat_history

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
	"elbot/internal/workspace"
	"encoding/json"
	"fmt"
	"strings"
)

type ViewImageTool struct{ Deps }
type viewImageArgs struct {
	Source     string      `json:"source"`
	MessageIDs historySeqs `json:"message_id"`
	MediaIndex [][]int     `json:"media_index"`
}

func (t ViewImageTool) Definition() toolshare.ToolDefinition {
	definition := viewImageBuilder().Build(t.PrepareCall)
	properties := definition.Parameters["properties"].(map[string]any)
	properties["message_id"].(map[string]any)["minItems"] = 1
	properties["media_index"] = map[string]any{
		"type": "array", "minItems": 1,
		"description": "长度同message_id；第i项选消息i的图片媒体编号，沿用历史编号，1-based；省略→每消息首图。",
		"items":       map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 1}},
	}
	return definition
}
func viewImageBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("view_image").
		Description("看图；source ⊻ message_id；source⇒禁传media_index。当前上下文已可见则无需重复调用。").
		APITypes(llm.APITypeResponse).Risk(toolshare.RiskLow).Tags("chat", "files").
		String("source", "media ID / HTTP(S) / 本地路径 / file URI。").
		StringArray("message_id", "当前聊天的历史消息短号，如 h8121，h 可省略。")
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
	return args, validateMediaIndex(args.MessageIDs, args.MediaIndex)
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
	path, err := t.LocalSourcePath(source)
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
	if path.Data.Path != "" && t.SensitivePath(path.Data.Path) {
		assessment = toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"读取可能包含凭据的敏感文件，需要用户确认"}}
	}
	details := []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "source", Label: "图片来源", Value: args.Source},
		toolshare.ConfirmationField{Key: "path", Label: "本地路径", Value: path.Data.Path},
		toolshare.ConfirmationField{Key: "message_id", Label: "消息", Value: args.MessageIDs},
		toolshare.ConfirmationField{Key: "media_index", Label: "媒体编号", Value: args.MediaIndex},
	)}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		if args.Source == "" {
			return t.viewMessages(ctx, args)
		}
		var err error
		var item operationresult.Result[toolshare.MediaInfo]
		if strings.HasPrefix(args.Source, media.IDPrefix) {
			item, err = t.Media.ValidateImage(ctx, args.Source)
		} else {
			if delivery.IsHTTPMediaSource(args.Source) {
				item, err = t.Media.ImportURL(ctx, args.Source, toolshare.MediaImportOptions{})
			} else {
				item, err = t.Media.ImportFile(ctx, path.Data.Path, toolshare.MediaImportOptions{})
			}
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if item.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: item.Status, Message: item.Message}, item.Message, nil
			}
			item, err = t.Media.ValidateImage(ctx, item.Data.ID)
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
	results, err := t.getMedia(ctx, MediaRequest{Seqs: args.MessageIDs, MediaIndex: args.MediaIndex}, true)
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
		label := fmt.Sprintf("[h%d] %d.", result.Seq, result.Index)
		if result.Err != nil {
			return operationresult.Result[*toolshare.Result]{}, result.Err.Error(), result.Err
		}
		if result.Media.Status != operationresult.Success {
			label += " " + result.Media.Message
		} else {
			item, err := t.Media.ValidateImage(ctx, result.Media.Data.ID)
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
	logContent := fmt.Sprintf("requested_messages=%d returned_items=%d failed_items=%d", len(args.MessageIDs), len(results.Data), failed)
	if failed > 0 && failed == len(results.Data) {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: llm.SegmentsContentText(segments)}, logContent, nil
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Segments: segments}}, logContent, nil
}

func imageResultSegment(item toolshare.MediaInfo) llm.MessageSegment {
	return llm.MessageSegment{Type: llm.SegmentImage, MediaID: item.ID, Name: item.Name, MIMEType: item.MIMEType}
}

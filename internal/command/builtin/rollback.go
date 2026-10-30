package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type FileRollbackService interface {
	List(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, uint64) (fileops.RollbackResult, error)
}

type rollbackCommand struct{ deps Deps }

func NewRollback(deps Deps) command.Handler { return rollbackCommand{deps: deps} }

func (rollbackCommand) Info() command.Info {
	return command.Info{
		Name: "rollback", Usage: "/rollback [编号]",
		Description: "列出文件编辑备份，或撤销指定编号的修改。",
		Help:        "每个文件仅保留最近一次 edit_file 修改。无参数列出编号，指定编号直接撤销；切换会话、重启或容量淘汰后备份失效。撤销新建文件会删除该文件。不支持 shell 修改。",
		MinRole:     contextinfo.RoleSuperadmin,
	}
}

func (c rollbackCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	if c.deps.FileRollback == nil || c.deps.PrepareFileContext == nil {
		return nil, fmt.Errorf("file rollback is not configured")
	}
	arg := strings.TrimSpace(req.Args)
	if arg == "" {
		records, err := c.list(ctx)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return &command.Result{Content: "当前会话没有可撤销的文件编辑。"}, nil
		}
		var b strings.Builder
		b.WriteString("当前会话可撤销的文件：\n")
		for _, record := range records {
			action := "恢复编辑前内容"
			if record.Created {
				action = "删除新建文件"
			}
			fmt.Fprintf(&b, "%d  %s  %s  %s\n", record.ID, record.Path, formatTime(record.EditedAt), action)
		}
		fmt.Fprintf(&b, "\n使用 %srollback <编号> 撤销。备份在切换会话、重启或容量淘汰后失效。", req.Prefix)
		return &command.Result{Content: b.String()}, nil
	}
	id, err := strconv.ParseUint(arg, 10, 64)
	if err != nil || id == 0 {
		return nil, fmt.Errorf("用法：%srollback [编号]；先不带参数列出文件", req.Prefix)
	}
	ctx, err = c.deps.PrepareFileContext(ctx, true)
	if errors.Is(err, session.ErrNoCurrentSession) {
		return nil, err
	}
	if err != nil {
		return &command.Result{Content: fmt.Sprintf("无法撤销：%v\n使用 %srollback 查看当前可撤销记录。", err, req.Prefix)}, nil
	}
	result, err := c.deps.FileRollback.RollbackByID(ctx, id)
	actor, _ := contextinfo.ActorFromContext(ctx)
	binding, _ := session.BindingFromContext(ctx)
	if result.Path != "" {
		sessionID := ""
		if binding != nil {
			sessionID = binding.SessionID()
		}
		content := map[string]any{"actor_id": actor.ID, "path": result.Path, "deleted": result.Deleted}
		record := globalevents.LogRecord{Category: globalevents.LogAudit, Module: "command", Level: slog.LevelInfo, EventName: "file_rollback", Type: globalevents.ContentJSON, Identity: globalevents.LogIdentity{SessionID: &sessionID}, ResultStatus: globalevents.ResultSucceeded}
		if err != nil {
			content["error"] = err.Error()
			record.EventName = "file_rollback_failed"
			record.Level = slog.LevelError
			record.ResultStatus = globalevents.ResultFailed
			if errors.Is(err, context.Canceled) {
				record.Level = slog.LevelInfo
				record.ResultStatus = globalevents.ResultCanceled
			}
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		data, _ := json.Marshal(content)
		record.Content = string(data)
		_ = globalevents.EmitLog(ctx, record)
	}
	if err != nil {
		return &command.Result{Content: fmt.Sprintf("无法撤销：%v\n使用 %srollback 查看当前可撤销记录。", err, req.Prefix)}, nil
	}
	if result.Deleted {
		return &command.Result{Content: fmt.Sprintf("已撤销新建文件：%s（父目录保留）。", result.Path)}, nil
	}
	return &command.Result{Content: fmt.Sprintf("已恢复编辑前内容：%s\nrevision: %s", result.Path, result.Revision)}, nil
}

func (c rollbackCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	if c.deps.FileRollback == nil || c.deps.PrepareFileContext == nil {
		return nil
	}
	token := command.CurrentCompletionToken(req)
	if !command.IsFirstArg(req, token) {
		return nil
	}
	records, err := c.list(ctx)
	if err != nil {
		return nil
	}
	options := make([]command.CompletionOption, 0, len(records))
	for _, record := range records {
		options = append(options, command.CompletionOption{Text: strconv.FormatUint(record.ID, 10), Description: record.Path})
	}
	return command.CompleteStaticOptions(options, token.Text, token.Start, token.End, "rollback_record")
}

func (c rollbackCommand) list(ctx context.Context) ([]fileops.RollbackInfo, error) {
	ctx, err := c.deps.PrepareFileContext(ctx, false)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c.deps.FileRollback.List(ctx)
}

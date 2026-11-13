package transfer

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"elbot/internal/delivery"
)

// ResolveForOutput 在持有期间准备发送临时文件，不提供签名 URL。
// 使用记录持有临时目录和媒体；准备失败由 Media 收尾。
func (m *Manager) ResolveForOutput(ctx context.Context, id string) (result operationresult.Result[delivery.Source], usageID string, err error) {
	retained, usageID, err := m.Hold(ctx, id)
	if err != nil {
		return operationresult.Result[delivery.Source]{}, "", err
	}
	if retained.Status != operationresult.Success {
		return operationresult.Result[delivery.Source]{Status: retained.Status, Message: retained.Message}, "", nil
	}
	held := retained.Data
	defer func() {
		if err != nil || result.Status != operationresult.Success {
			m.abortUsage(ctx, usageID)
			usageID = ""
		}
	}()
	dir, err := os.MkdirTemp("", "elbot-output-*")
	if err != nil {
		return operationresult.Result[delivery.Source]{}, usageID, err
	}
	m.usagesMu.Lock()
	u := m.usages[usageID]
	m.usagesMu.Unlock()
	u.mu <- struct{}{}
	u.dirs = append(u.dirs, dir)
	<-u.mu
	reader, err := held.Open(ctx)
	if err != nil {
		return operationresult.Result[delivery.Source]{}, usageID, err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			m.options.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_reader_close_failed", Module: "media", Content: closeErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(closeErr)})
		}
	}()
	path := filepath.Join(dir, "media")
	file, err := os.Create(path)
	if err != nil {
		return operationresult.Result[delivery.Source]{}, usageID, err
	}
	_, err = io.Copy(file, reader)
	if closeErr := file.Close(); closeErr != nil {
		if err == nil {
			return operationresult.Result[delivery.Source]{}, usageID, closeErr
		}
		m.options.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_file_close_failed", Module: "media", Content: closeErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(closeErr)})
	}
	if err != nil {
		return operationresult.Result[delivery.Source]{}, usageID, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[delivery.Source]{}, usageID, err
	}
	return operationresult.Result[delivery.Source]{Status: operationresult.Success, Data: delivery.Source{MediaID: id, Path: path, MIMEType: held.Info().MIMEType}}, usageID, nil
}

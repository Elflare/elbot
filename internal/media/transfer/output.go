package transfer

import (
	"context"
	"elbot/internal/events"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"elbot/internal/delivery"
)

// ResolveForOutput 在持有期间准备发送临时文件，不提供签名 URL。
// 使用记录持有临时目录和媒体；准备失败由 Media 收尾。
func (m *Manager) ResolveForOutput(ctx context.Context, id string) (_ delivery.Source, usageID string, err error) {
	held, usageID, err := m.Hold(ctx, id)
	if err != nil {
		return delivery.Source{}, "", err
	}
	defer func() {
		if err != nil {
			m.abortUsage(ctx, usageID)
			usageID = ""
		}
	}()
	dir, err := os.MkdirTemp("", "elbot-output-*")
	if err != nil {
		return delivery.Source{}, usageID, err
	}
	m.usagesMu.Lock()
	u := m.usages[usageID]
	m.usagesMu.Unlock()
	u.mu <- struct{}{}
	u.dirs = append(u.dirs, dir)
	<-u.mu
	reader, err := held.Open(ctx)
	if err != nil {
		return delivery.Source{}, usageID, err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			m.options.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_reader_close_failed", Module: "media", Content: closeErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(closeErr)})
		}
	}()
	path := filepath.Join(dir, "media")
	file, err := os.Create(path)
	if err != nil {
		return delivery.Source{}, usageID, err
	}
	_, err = io.Copy(file, reader)
	if closeErr := file.Close(); closeErr != nil {
		if err == nil {
			return delivery.Source{}, usageID, closeErr
		}
		m.options.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_file_close_failed", Module: "media", Content: closeErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(closeErr)})
	}
	if err != nil {
		return delivery.Source{}, usageID, err
	}
	if err := ctx.Err(); err != nil {
		return delivery.Source{}, usageID, err
	}
	return delivery.Source{MediaID: id, Path: path, MIMEType: held.Info().MIMEType}, usageID, nil
}

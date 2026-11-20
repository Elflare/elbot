package object

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"elbot/internal/media/share"
	"elbot/internal/storage"
)

func closeMediaReader(ctx context.Context, reader io.Closer) {
	if err := reader.Close(); err != nil {
		events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_reader_close_failed", Module: "media", Content: err.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)})
	}
}

// Info 取得通用信息快照，不读取内容或增加引用。
func (m *Manager) Info(ctx context.Context, id string) (operationresult.Result[share.MediaInfo], error) {
	if !share.ValidID(id) {
		return operationresult.Result[share.MediaInfo]{Status: operationresult.InvalidArguments, Message: "invalid media ID"}, nil
	}
	item, err := m.media.Get(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return operationresult.Result[share.MediaInfo]{Status: operationresult.NotFound, Message: "media unavailable"}, nil
		}
		return operationresult.Result[share.MediaInfo]{}, err
	}
	if item.Deleting {
		return operationresult.Result[share.MediaInfo]{Status: operationresult.NotFound, Message: "media unavailable"}, nil
	}
	return operationresult.Result[share.MediaInfo]{Status: operationresult.Success, Data: mediaInfo(item)}, nil
}

// mediaInfo 只投影通用信息，不兼容修正已有记录。
func mediaInfo(item *storage.Media) share.MediaInfo {
	return share.MediaInfo{ID: item.ID, Name: item.Name, MIMEType: item.MIMEType, Size: item.Size}
}

// Read 读取本次持有的内容，不另取引用。
func (h *heldMedia) Read(ctx context.Context) ([]byte, error) {
	reader, err := h.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer closeMediaReader(ctx, reader)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// Export 写入目标文件，目标关闭失败属于写入失败。
func (h *heldMedia) Export(ctx context.Context, destination string) error {
	reader, err := h.Open(ctx)
	if err != nil {
		return err
	}
	defer closeMediaReader(ctx, reader)
	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("create media export: %w", err)
	}
	_, err = io.Copy(file, reader)
	if closeErr := file.Close(); closeErr != nil {
		if err == nil {
			return closeErr
		}
		events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_file_close_failed", Module: "media", Content: closeErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(closeErr)})
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (m *Manager) backendForStoredMedia(item *storage.Media) (share.Backend, error) {
	switch item.Backend {
	case "local":
		return m.local, nil
	case "s3":
		if m.remote == nil {
			return nil, fmt.Errorf("remote media backend is unavailable")
		}
		return m.remote, nil
	default:
		return nil, fmt.Errorf("unsupported stored media backend %q", item.Backend)
	}
}

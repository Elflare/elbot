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

// Read 读取完整内容；读取器和引用释放故障独立记录。
// 释放失败的自有持有交给 Cleanup 重试。
func (m *Manager) Read(ctx context.Context, id string) (result operationresult.Result[[]byte], info share.MediaInfo, err error) {
	retained, err := m.Hold(ctx, id)
	if err != nil {
		return operationresult.Result[[]byte]{}, share.MediaInfo{}, err
	}
	if retained.Status != operationresult.Success {
		return operationresult.Result[[]byte]{Status: retained.Status, Message: retained.Message}, share.MediaInfo{}, nil
	}
	held := retained.Data
	defer m.releaseOwned(ctx, held)
	info = held.Info()
	reader, err := held.Open(ctx)
	if err != nil {
		return operationresult.Result[[]byte]{}, share.MediaInfo{}, err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_reader_close_failed", Module: "media", Content: closeErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(closeErr)})
		}
	}()
	data, err := io.ReadAll(reader)
	if err != nil {
		return operationresult.Result[[]byte]{}, share.MediaInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[[]byte]{}, share.MediaInfo{}, err
	}
	return operationresult.Result[[]byte]{Status: operationresult.Success, Data: data}, info, nil
}

// Export 将对象写入目标文件并释放本次持有。
// 释放失败的自有持有交给 Cleanup 重试。
func (m *Manager) Export(ctx context.Context, id, destination string) (result operationresult.Result[share.MediaInfo], err error) {
	retained, err := m.Hold(ctx, id)
	if err != nil {
		return operationresult.Result[share.MediaInfo]{}, err
	}
	if retained.Status != operationresult.Success {
		return operationresult.Result[share.MediaInfo]{Status: retained.Status, Message: retained.Message}, nil
	}
	held := retained.Data
	defer m.releaseOwned(ctx, held)
	info := held.Info()
	reader, err := held.Open(ctx)
	if err != nil {
		return operationresult.Result[share.MediaInfo]{}, err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_reader_close_failed", Module: "media", Content: closeErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(closeErr)})
		}
	}()
	file, err := os.Create(destination)
	if err != nil {
		return operationresult.Result[share.MediaInfo]{}, fmt.Errorf("create media export: %w", err)
	}
	_, err = io.Copy(file, reader)
	if closeErr := file.Close(); closeErr != nil {
		if err == nil {
			return operationresult.Result[share.MediaInfo]{}, closeErr
		}
		events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_file_close_failed", Module: "media", Content: closeErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(closeErr)})
	}
	if err != nil {
		return operationresult.Result[share.MediaInfo]{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[share.MediaInfo]{}, err
	}
	return operationresult.Result[share.MediaInfo]{Status: operationresult.Success, Data: info}, nil
}

// backendForStoredMedia 按内部记录选择后端，不向调用者暴露记录。
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

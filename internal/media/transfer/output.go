package transfer

import (
	"context"
	"elbot/internal/media/share"
	"fmt"
	"os"

	"elbot/internal/delivery"
	"elbot/internal/operationresult"
)

func outputSource(ctx context.Context, held share.HeldMedia) (delivery.Source, error) {
	info := held.Info()
	source := delivery.Source{MediaID: info.ID, MIMEType: info.MIMEType, Path: held.LocalPath()}
	if source.Path != "" {
		if _, err := os.Stat(source.Path); os.IsNotExist(err) {
			source.Path = ""
		} else if err != nil {
			return delivery.Source{}, err
		}
	}
	if source.Path == "" {
		var err error
		source.URL, err = held.SignedURL(ctx)
		if err != nil {
			return delivery.Source{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return delivery.Source{}, err
	}
	return source, nil
}

// OutputRequest 是输出序列中一个需要交付的媒体项。
type OutputRequest struct {
	Index  int             // 原输出下标，非媒体项不进入批次。
	Name   string          // 调用方指定的展示名；空时采用归档名称。
	Source delivery.Source // 已归档 ID 或待归档的来源。
}

// PreparedOutput 保留原下标，Source 同时携带规范 ID 和发送源。
type PreparedOutput struct {
	Index  int
	Name   string
	Source delivery.Source
}

// PrepareOutputs 按输入顺序准备；仅成功时才交付批次持有。
func (m *Manager) PrepareOutputs(ctx context.Context, requests []OutputRequest) (result operationresult.Result[[]PreparedOutput], usage string, err error) {
	defer func() {
		if usage != "" && (err != nil || result.Status != operationresult.Success) {
			m.abortUsage(ctx, usage)
			usage = ""
		}
	}()
	outputs := make([]PreparedOutput, 0, len(requests))
	for _, req := range requests {
		id := req.Source.MediaID
		if id == "" {
			item, err := m.ImportSource(ctx, req.Source, share.Input{Name: req.Name, MIMEType: req.Source.MIMEType}, share.ImportLimits{})
			if err != nil {
				return result, usage, fmt.Errorf("prepare output media %d: %w", req.Index, err)
			}
			if item.Status != operationresult.Success {
				result.Status, result.Message = item.Status, item.Message
				return result, usage, nil
			}
			id = item.Data.ID
		}
		item, err := m.objects.Hold(ctx, id)
		if err != nil {
			return result, usage, err
		}
		if item.Status != operationresult.Success {
			result.Status, result.Message = item.Status, item.Message
			return result, usage, nil
		}
		held := item.Data
		usage = m.addHeld(usage, held)
		source, err := outputSource(ctx, held)
		if err != nil {
			return result, usage, err
		}
		name := held.Info().Name
		if req.Name != "" {
			name = share.SanitizeName(req.Name)
		}
		outputs = append(outputs, PreparedOutput{Index: req.Index, Name: name, Source: source})
	}
	return operationresult.Result[[]PreparedOutput]{Status: operationresult.Success, Data: outputs}, usage, nil
}

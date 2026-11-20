package transfer

import (
	"context"
	"os"

	"elbot/internal/delivery"
	"elbot/internal/operationresult"
)

// ResolveForOutput 只借用归档文件或签名 URL；持有持续到发送结束。
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
		if err != nil {
			m.abortUsage(ctx, usageID)
			usageID = ""
		}
	}()
	source := delivery.Source{MediaID: id, MIMEType: held.Info().MIMEType, Path: held.LocalPath()}
	if source.Path != "" {
		if _, err := os.Stat(source.Path); os.IsNotExist(err) {
			source.Path = ""
		} else if err != nil {
			return operationresult.Result[delivery.Source]{}, usageID, err
		}
	}
	if source.Path == "" {
		source.URL, err = held.SignedURL(ctx)
		if err != nil {
			return operationresult.Result[delivery.Source]{}, usageID, err
		}
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[delivery.Source]{}, usageID, err
	}
	return operationresult.Result[delivery.Source]{Status: operationresult.Success, Data: source}, usageID, nil
}

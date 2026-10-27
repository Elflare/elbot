// public_api.go 提供对外通用能力调用与原生 API 透传入口。

package platform

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/platform/share"
	"encoding/json"
)

// CallCapability 只调用 ElBot 明确实现的通用能力；未知能力不转入原生透传。
func (m *Supervisor) CallCapability(ctx context.Context, platform, name string, params map[string]any) (json.RawMessage, error) {
	return m.dispatcher.CallCapability(ctx, platform, name, params)
}

// CallPlatformAPI 转交平台原生接口名称与参数，不识别、映射或登记具体接口。
func (m *Supervisor) CallPlatformAPI(ctx context.Context, platform, api string, params map[string]any) (json.RawMessage, error) {
	return m.dispatcher.CallPlatformAPI(ctx, platform, api, params)
}

// ResolveMedia 使用历史消息所属平台的解析能力，不读取当前消息上下文。
func (m *Supervisor) ResolveMedia(ctx context.Context, platform string, input media.PlatformMedia, maxBytes int64) (delivery.Source, error) {
	return m.dispatcher.ResolveMedia(ctx, platform, share.MessageSegment{Type: share.MessageSegmentType(input.Type),
		MediaID: input.MediaID, URL: input.URL, PlatformFileID: input.PlatformFileID, MIMEType: input.MIMEType, Name: input.Name, Size: input.Size}, maxBytes)
}

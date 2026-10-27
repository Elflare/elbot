// capability_api.go 提供平台原生 API 入口，负责协议调用与传输。
package qqonebot

import (
	"context"
	"encoding/json"
	"strings"

	"elbot/internal/platform/share"
)

var _ share.PlatformAPICaller = (*Adapter)(nil)

func (a *Adapter) CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error) {
	resp, err := a.transport.Call(ctx, strings.TrimSpace(api), params)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

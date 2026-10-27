// capability_api.go 提供平台原生 API 入口，负责协议调用与传输。
package telegram

import (
	"context"
	"encoding/json"
	"strings"

	"elbot/internal/platform/share"
)

var _ share.PlatformAPICaller = (*Adapter)(nil)

func (a *Adapter) CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error) {
	ctx, finish, err := a.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	return a.client.callRaw(ctx, strings.TrimSpace(api), params)
}

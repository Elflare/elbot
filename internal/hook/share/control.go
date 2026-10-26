// 定义 Hook 子包之间实际使用的流水线管理与运行时协作能力。
package share

import (
	"context"
	"encoding/json"

	"elbot/internal/events"
)

// PipelineControl 供 control 查询和停止活动规则，不暴露执行器或注册表。
type PipelineControl interface {
	List() []Info
	Matches(string) bool
	Stop(string) int
}

// RuntimeControl 供 control 协调插件配置提交与进程管理。
type RuntimeControl interface {
	Apply([]RuntimeConfig, func(string)) error
	ValidatePlugin(RuntimeConfig) error
	ReplacePlugin(string, *RuntimeConfig, func()) (events.LogResult, error)
	List() []RuntimeInfo
	Start(string) error
	Stop(context.Context, string) (bool, error)
	Restart(context.Context, string) error
}

// RuntimeCaller 供 rules 发起插件处理、媒体请求和共享状态请求。
type RuntimeCaller interface {
	Handle(context.Context, string, Event, Control) (Event, events.LogResult, error)
	MediaRequest(context.Context, string, string, json.RawMessage) (any, error)
	SharedRequest(string, json.RawMessage) (any, error)
}

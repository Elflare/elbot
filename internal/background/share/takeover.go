package share

import "context"

// IsSessionForeground 查询目标会话当前是否为前台，由根部注入 Agent 的公开能力。
// 空标识或不存在的会话返回 false；读取失败正常返回错误。
type IsSessionForeground func(context.Context, string) (bool, error)

package share

import "context"

// SessionTakenOver 查询后台会话是否已被前台接管，由根部注入 Agent 的公开能力。
// 空标识或不存在的会话返回 false；读取失败正常返回错误。
type SessionTakenOver func(context.Context, string) (bool, error)

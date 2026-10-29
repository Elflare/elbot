// 职责：声明 Session 根部消费的存储、模型、活跃状态及材料准备等外部能力契约。
// 按实际需求接入依赖，由 wiring.go 裁剪并注入内部组件。
package session

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

// SupervisorDeps 由 Agent 提供；所有资源均为借用，Session 不负责关闭。
type SupervisorDeps struct {
	Store            storage.Store                                 // 根部提取仓库能力，不向子包交付整个 Store。
	Models           ModelCapabilities                             // 仅提供命名选择，不交付模型管理操作。
	ActiveSessionIDs func() []string                               // 查询活跃会话事实，状态仍归 Agent 的执行组件。
	CheckForeground  func(context.Context, *storage.Session) error // 接管准入内的兼容性检查，不得执行 I/O 或再次获取准入。
	Materials        MaterialResolver                              // 构造时只接入，业务启动前由 Agent 完成协议路线注册。
	IsSuperadmin     func(Scope) bool                              // 根据绑定的范围身份查询权限。
	ReleaseSession   func(string)                                  // 同步释放 Agent 自有闲置会话状态；不得 I/O、等待或重新准入。
	ReleaseBinding   func(*Binding)                                // 同步释放原绑定的撤销等状态；释放操作均须幂等。
}

// ModelCapabilities 固定命名任务所需的模型选择能力。
type ModelCapabilities interface {
	ResolveNaming(context.Context) modelmgr.NamingSelection
}

// MaterialResolver 按会话的协议归属取得材料准备能力。
type MaterialResolver interface {
	MaterialFor(llm.Origin) (MaterialPreparer, error)
}

// MaterialPreparer 在准入锁外准备分支快照；无原生材料时允许返回 nil。
// PreparedMaterial 的公开结果定义由 contracts.go 提供。
type MaterialPreparer interface {
	PrepareFork(context.Context, *storage.Session, *storage.Message) (*PreparedMaterial, error)
	PrepareCopy(context.Context, *storage.Session) (*PreparedMaterial, error)
}

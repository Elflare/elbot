// 职责：定义对外公开的 Scope、会话请求、结果及事件类型。
// 内部组件独用的定义留在所属子包，多个子包共用的契约放入 share。
package session

import (
	"elbot/internal/contextinfo"
	"elbot/internal/session/control"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

var (
	ErrSessionBusy                  = share.ErrSessionBusy
	ErrForegroundSession            = share.ErrForegroundSession
	ErrChatModeRequiresEmptySession = control.ErrChatModeRequiresEmptySession
)

// Scope 标识前台会话所属的用户和平台范围。
type Scope struct {
	ConversationKind contextinfo.ConversationKind // 用于判断群聊、私聊等接入语义。
	ActorID          string                       // 会话所属用户。
	Platform         string                       // 平台标识。
	PlatformScopeID  string                       // 平台内的会话范围。
	IsCLI            bool                         // 本地管理入口，可跨用户、平台和范围管理会话。
}

// Key 返回 current 绑定的范围身份，不包含可变的会话状态。
func (s Scope) Key() string { return share.Scope(s).Key() }

type CreateRequest struct {
	ID       string // 执行交接可预分配 ID，以便同时持有源和目标的准入。
	Title    string // 新会话标题。
	Mode     string // 新会话模式；默认值由 control 应用。
	Metadata string // 会话元数据 JSON。
}

type ActivateModeRequest struct {
	Mode            string // 目标前台模式。
	NewSessionTitle string // 需要新建会话时使用的标题。
}

type ActivateModeResult struct {
	Session       *storage.Session // 激活后的会话快照。
	AlreadyActive bool             // 当前会话原本已使用目标模式。
}

type Status struct {
	Session           *storage.Session // 当前会话快照。
	MessageCount      int              // 当前会话自己的消息数。
	LastUserContent   string           // 最近一条非空用户正文。
	LastAnswerContent string           // 最近一条非空助手正文。
}

// BackgroundRequest 准备任务会话，不激活前台绑定，也不承载工具运行状态。
type BackgroundRequest struct {
	SessionID string            // 非空时复用指定的未接管后台会话。
	Kind      string            // 后台来源种类。
	Name      string            // 后台任务名称。
	Title     string            // 新会话标题。
	Metadata  map[string]string // 后台业务附加字段。
}

// BackgroundCopyRequest 复制已提交的后台历史，不继承执行和工具状态。
type BackgroundCopyRequest struct {
	SourceSessionID string                  // 待复制的后台会话。
	Kind            string                  // 副本的后台来源种类。
	Name            string                  // 副本所属任务名称。
	Title           string                  // 副本标题。
	Metadata        storage.SessionMetadata // 业务字段；命名和后台身份由 Session 填写。
}

// CompactedRequest 承接上下文所有者准备的结果；继承和原子保存由 Session 负责。
type CompactedRequest struct {
	ID                   string              // 与源会话不同的预分配目标 ID。
	Title                string              // 压缩后会话标题。
	Metadata             string              // 待合并的会话元数据 JSON。
	Seed                 *storage.NativeSeed // 原生压缩材料；无原生材料时为 nil。
	ExpectedCheckpointID string              // 提交时复核的源会话 checkpoint。
}

// PreparedMaterial 是协议路线在准入锁外准备的不可变分支材料。
type PreparedMaterial struct {
	Seed                 *storage.NativeSeed // 新会话的原生种子。
	ExpectedCheckpointID string              // 材料对应的源 checkpoint，提交时用于复核。
}

type BindingChangedEvent struct {
	Version uint64   // 已提交的变更版本；解除绑定也推进版本。
	Old     *Binding // 旧绑定；切离后永久失效。
	New     *Binding // 新绑定；解除绑定时为 nil。
}

// ForegroundActivatedEvent 在接管提交后、释放准入前同步交付。
// 接收方只能更新执行内存，不得执行 I/O 或再次获取 Scope／Session 准入。
type ForegroundActivatedEvent struct {
	Session storage.Session // 已提交的快照；接收方不得修改其中的指针字段。
	Binding *Binding        // 本次接管建立的前台绑定。
}

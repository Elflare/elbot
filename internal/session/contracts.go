// 职责：定义对外公开的 Scope、会话请求、结果及事件类型。
// 内部组件独用的定义留在所属子包，多个子包共用的契约放入 share。
package session

import (
	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

// Scope 标识前台会话所属的用户和平台范围。
type Scope struct {
	TaskType     string                    // 后台任务类型；前台聊天为空。
	TaskID       string                    // 类型内的原生任务 ID。
	ChatType     contextinfo.ChatType      // 用于判断群聊、私聊等接入语义。
	Owner        contextinfo.ActorIdentity // 会话所有者，独立于会话投递平台。
	Platform     string                    // 平台标识。
	ChatID       string                    // 平台内的会话范围。
	IsSuperadmin bool                      // 已解析的超级管理员角色，用于会话访问裁决。
}

// CurrentBoundData 携带同一次准入读取的 Session 快照与绑定句柄。
type CurrentBoundData struct {
	Session *storage.Session
	Binding *Binding
}

// Key 返回 current 绑定的范围身份，不包含可变的会话状态。
type ScopeKey = share.ScopeKey

func (s Scope) Key() ScopeKey { return share.Scope(s).Key() }

type CreateRequest struct {
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

type ListRequest struct {
	Query          string // 标题搜索词，空值不筛选。
	ArchivedOnly   bool   // 仅查询已归档会话。
	ExcludeCurrent bool   // 排除当前绑定的会话。
	Page           int    // 从 1 开始；0 表示最后一页。
	PageSize       int    // 每页会话数量。
	AllPlatforms   bool   // 经上层授权后查询所有平台的会话。
}

type ListResult struct {
	Items      []storage.SessionSummary // 按全局顺序排列的当前页原始摘要。
	Page       int                      // 实际页码，从 1 开始。
	PageSize   int                      // 每页容量。
	TotalCount int                      // 筛选后的会话总数。
	TotalPages int                      // 总页数，空列表为 0。
}

// BackgroundRequest 准备任务会话，不激活前台绑定，也不承载工具运行状态。
type BackgroundRequest struct {
	TargetPlatforms []string          // 创建时解析出的有效投递平台；ID 使用其去重快照。
	SessionID       string            // 非空时复用指定的未接管后台会话。
	TaskType        string            // 后台来源种类。
	TaskID          string            // 原生任务 ID。
	Name            string            // 新会话标题。
	Metadata        map[string]string // 后台业务附加字段。
}

// BackgroundCopyRequest 复制已提交的后台历史，不继承执行和工具状态。
type BackgroundCopyRequest struct {
	SourceSessionID string                  // 待复制的后台会话。
	TaskType        string                  // 副本的后台来源种类。
	TaskID          string                  // 副本所属任务的原生 ID。
	Name            string                  // 副本标题。
	Metadata        storage.SessionMetadata // 业务字段；命名和后台身份由 Session 填写。
}

// CompactedRequest 承接上下文所有者准备的结果；继承和原子保存由 Session 负责。
type CompactedRequest struct {
	ID                   string              // 与源会话不同的预分配目标 ID。
	Title                string              // 压缩后会话标题。
	Metadata             string              // 待合并的会话元数据 JSON。
	InitialMessage       string              // Chat 压缩后写入新会话的合成首条用户消息；Responses 留空。
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

// CanAccess 判断已读取会话是否属于给定访问范围，不授予执行准入。
func CanAccess(scope Scope, row *storage.Session) bool {
	return share.CanAccess(share.Scope(scope), row)
}

// Package object 管理媒体记录、内容、引用与回收。
package object

import (
	"context"
	"sync"
	"time"

	"elbot/internal/config"
	"elbot/internal/media/share"
	"elbot/internal/storage"
)

// Options 是根部从完整配置拆出的对象配置。
type Options struct {
	Backend         string             // 导入位置，根部规范化为 local 或 s3。
	MaxImportBytes  int64              // 单个导入内容的字节上限。
	DownloadTimeout time.Duration      // 默认下载期限。
	Media           config.MediaConfig // 图片压缩阈值及最大边长。
	Now             func() time.Time   // 时间来源，用于访问、过期及签名缓存。
}

// Deps 是对象管理借用的仓储和后端，不包含完整 Store。
type Deps struct {
	Media      MediaRepository     // 对象、缓存及历史关联仓储。
	References ReferenceRepository // 临时及持久引用的添加和释放。
	Local      share.Backend       // 本地对象读写。
	Remote     share.RemoteBackend // 延迟初始化的远端对象及签名能力，可无配置。
}

// Manager 持有对象状态；不保存构造后才注入的历史查询。
type Manager struct {
	options         Options             // 构造时固定的对象配置。
	media           MediaRepository     // 借用的对象及关联仓储。
	references      ReferenceRepository // 借用的引用仓储。
	local           share.Backend       // 本地内容后端。
	remote          share.RemoteBackend // 远端内容及签名后端。
	objects         objectLocks         // 按媒体 ID 串行对象变更。
	cleanupMu       objectLocks         // 串行清理轮次，不阻塞不同对象的普通操作。
	pendingReleases []share.HeldMedia   // 便捷方法已结束使用、但尚未释放成功的自有持有。
	releasesMu      sync.Mutex          // 保护待释放集合；重试期间不持锁。
}

// MediaRepository 仅包含对象管理实际使用的存储能力。
type MediaRepository interface {
	SaveHistory(ctx context.Context, item storage.HistoryMedia) error
	FindHistory(ctx context.Context, platform, scopeID, messageID string) ([]storage.HistoryMedia, error)
	ListHistory(ctx context.Context, afterOwnerID string, limit int) ([]storage.HistoryMedia, error)
	DeleteHistory(ctx context.Context, ownerID string) error
	Get(ctx context.Context, id string) (*storage.Media, error)
	Upsert(ctx context.Context, media *storage.Media) error
	GetSignedURL(ctx context.Context, mediaID string) (*storage.MediaSignedURL, error)
	UpsertSignedURL(ctx context.Context, record *storage.MediaSignedURL) error
	ClaimOrphans(ctx context.Context, cutoff time.Time) ([]storage.Media, error)
	FinishDelete(ctx context.Context, id string) error
	Touch(ctx context.Context, id string, now time.Time) error
	FindOutputs(ctx context.Context, platform, scopeID, messageID string, now time.Time) ([]storage.MediaOutput, error)
	ExpireOutputs(ctx context.Context, now time.Time) error
	CheckReferences(ctx context.Context) ([]string, error)
	RecoverInterrupted(ctx context.Context) error
}

// ReferenceRepository 不承接会话引用的事务提交。
type ReferenceRepository interface {
	Add(ctx context.Context, reference *storage.MediaReference) error
	Remove(ctx context.Context, reference storage.MediaReference) error
}

// History 只在对账、清理调用时借用，不保存完整历史服务。
type History interface {
	GetByPlatformMessage(ctx context.Context, platform, scopeID, messageID string) (*storage.ChatMessage, error)
}

// NewManager 保存裁剪后的配置、仓储和后端并执行中断恢复。
// 部分构造失败仍交回已创建的组件；历史对账由根部注入 History 后发起。
func NewManager(ctx context.Context, opts Options, deps Deps) (*Manager, error) {
	m := &Manager{options: opts, media: deps.Media, references: deps.References, local: deps.Local, remote: deps.Remote}
	return m, m.media.RecoverInterrupted(ctx)
}

// 在编译时核对跨组件契约。
var _ share.Objects = (*Manager)(nil)

package object

import (
	"context"
	"elbot/internal/media/share"
	"elbot/internal/storage"
	"io"
	"sync"
)

// Reference 是根部交付的持久引用身份，时间由本组件填写。
type Reference struct {
	MediaID   string // 被引用媒体。
	OwnerType string // 业务所有者类别。
	OwnerID   string // 业务所有者身份。
	Purpose   string // 引用用途。
}

// heldMedia 保存单个对象的持有状态，存储记录不越过 object 边界。
// 调用方结束全部读取和签名操作后再关闭句柄，不自动跟踪读取器数量。
type heldMedia struct {
	manager   *Manager               // 提供对象读取、签名与引用释放能力。
	record    storage.Media          // 已持有的内部记录，避免对外泄露后端和位置。
	reference storage.MediaReference // 本次持有独占的临时引用身份。
	released  bool                   // 仅在释放成功后置位，使重复关闭不重复释放。
}

// Hold 在对象变更边界内取得记录及临时引用，防止与回收竞争。
func (m *Manager) Hold(ctx context.Context, id string) (share.HeldMedia, error) { panic("TODO") }

// Info 返回持有记录的通用信息快照。
func (h *heldMedia) Info() share.MediaInfo { panic("TODO") }

// Open 更新访问事实并打开内容，不重复查询或添加引用。
func (h *heldMedia) Open(ctx context.Context) (io.ReadCloser, error) { panic("TODO") }

// Close 在独立清理期限内释放引用；失败保留引用，成功后重复调用无副作用。
func (h *heldMedia) Close() error { panic("TODO") }

// AddReference 添加持久业务引用，不承接会话事务。
func (m *Manager) AddReference(ctx context.Context, reference Reference) error { panic("TODO") }

// RemoveReference 移除指定业务身份的引用。
func (m *Manager) RemoveReference(ctx context.Context, reference Reference) error { panic("TODO") }

// referencedReader 负责便捷 Open 的读取器与持有收尾，显式句柄 Open 不使用它。
type referencedReader struct {
	io.ReadCloser                 // 本次实际内容读取器。
	held          share.HeldMedia // 本次读取取得的持有。
	once          sync.Once       // 只关闭实际读取器一次；持有释放不放入 Once，失败仍可重试。
	err           error           // 保留读取器关闭错误，避免重试丢失故障。
}

// Close 关闭读取器并释放持有；引用释放失败时仍允许重试。
func (r *referencedReader) Close() error { panic("TODO") }

var _ share.HeldMedia = (*heldMedia)(nil)

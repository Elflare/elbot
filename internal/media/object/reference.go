package object

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"elbot/internal/media/share"
	"elbot/internal/storage"
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
	mu        sync.Mutex             // 串行本句柄的释放，保护 released；不阻塞其他持有的对象操作。
}

// Hold 在对象变更边界内取得记录及临时引用，防止与回收竞争。
func (m *Manager) Hold(ctx context.Context, id string) (share.HeldMedia, error) {
	if !share.ValidID(id) {
		return nil, fmt.Errorf("invalid media ID")
	}
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	item, err := m.media.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	ref := storage.MediaReference{MediaID: id, OwnerType: "request", OwnerID: storage.NewID(), Purpose: "temporary", CreatedAt: m.options.Now()}
	// 存储原子拒绝对删除中对象添加引用，无需重复查询删除状态。
	if err := m.references.Add(ctx, &ref); err != nil {
		return nil, err
	}
	return &heldMedia{manager: m, record: *item, reference: ref}, nil
}

// Info 返回持有记录的通用信息快照。
func (h *heldMedia) Info() share.MediaInfo { return mediaInfo(&h.record) }

// Open 更新访问事实并打开内容，不重复查询或添加引用。
func (h *heldMedia) Open(ctx context.Context) (io.ReadCloser, error) {
	h.mu.Lock()
	if h.released {
		h.mu.Unlock()
		return nil, fmt.Errorf("media hold is closed")
	}
	h.mu.Unlock()
	backend, err := h.manager.backendForStoredMedia(&h.record)
	if err != nil {
		return nil, err
	}
	if err := h.manager.media.Touch(ctx, h.record.ID, h.manager.options.Now()); err != nil {
		return nil, err
	}
	location := h.record.LocalPath
	if h.record.Backend == "s3" {
		location = h.record.ObjectKey
	}
	return backend.Open(ctx, location)
}

// Close 在独立清理期限内释放引用；失败保留引用，成功后重复调用无副作用。
func (h *heldMedia) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.manager.references.Remove(cleanupCtx, h.reference); err != nil {
		return err
	}
	h.released = true
	return nil
}

// releaseOwned 结束便捷方法独占的持有，失败后将最终释放责任交给 Cleanup。
// 显式交给调用方的 Hold、Open 不经过此入口。
func (m *Manager) releaseOwned(held share.HeldMedia) error {
	err := held.Close()
	if err != nil {
		m.releasesMu.Lock()
		m.pendingReleases = append(m.pendingReleases, held)
		m.releasesMu.Unlock()
	}
	return err
}

// retryReleases 在清理轮次内重试一个快照；失败及未开始项留给后续轮次。
// 集合锁不覆盖关闭操作，重试期间新增的待释放项独立保留。
func (m *Manager) retryReleases(ctx context.Context) error {
	m.releasesMu.Lock()
	pending := m.pendingReleases
	m.pendingReleases = nil
	m.releasesMu.Unlock()
	var remaining []share.HeldMedia
	var failures []error
	for i, held := range pending {
		if ctx.Err() != nil {
			remaining = append(remaining, pending[i:]...)
			break
		}
		if err := held.Close(); err != nil {
			remaining = append(remaining, held)
			failures = append(failures, err)
		}
	}
	m.releasesMu.Lock()
	m.pendingReleases = append(m.pendingReleases, remaining...)
	m.releasesMu.Unlock()
	if len(failures) == 0 {
		return ctx.Err()
	}
	return errors.Join(append(failures, ctx.Err())...)
}

// AddReference 添加持久业务引用，不承接会话事务。
func (m *Manager) AddReference(ctx context.Context, reference Reference) error {
	if !share.ValidID(reference.MediaID) {
		return fmt.Errorf("invalid media ID")
	}
	return m.references.Add(ctx, &storage.MediaReference{MediaID: reference.MediaID, OwnerType: reference.OwnerType, OwnerID: reference.OwnerID, Purpose: reference.Purpose, CreatedAt: m.options.Now()})
}

// RemoveReference 移除指定业务身份的引用。
func (m *Manager) RemoveReference(ctx context.Context, reference Reference) error {
	if !share.ValidID(reference.MediaID) {
		return fmt.Errorf("invalid media ID")
	}
	return m.references.Remove(ctx, storage.MediaReference{MediaID: reference.MediaID, OwnerType: reference.OwnerType, OwnerID: reference.OwnerID, Purpose: reference.Purpose})
}

// referencedReader 负责便捷 Open 的读取器与持有收尾，显式句柄 Open 不使用它。
type referencedReader struct {
	io.ReadCloser                 // 本次实际内容读取器。
	held          share.HeldMedia // 本次读取取得的持有。
	once          sync.Once       // 只关闭实际读取器一次；持有释放不放入 Once，失败仍可重试。
	err           error           // 保留读取器关闭错误，避免重试丢失故障。
}

// Close 关闭读取器并释放持有；引用释放失败时仍允许重试。
func (r *referencedReader) Close() error {
	r.once.Do(func() {
		// 打开内容失败但引用释放也失败时，只保留待释放的持有。
		if r.ReadCloser != nil {
			r.err = r.ReadCloser.Close()
		}
	})
	return errors.Join(r.err, r.held.Close())
}

var _ share.HeldMedia = (*heldMedia)(nil)

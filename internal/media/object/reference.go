package object

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync/atomic"
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
	released  atomic.Bool            // 释放成功后置位。
	mu        chan struct{}          // 可取消地取得释放资格。
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
		if errors.Is(err, storage.ErrNotFound) {
			return nil, &share.UnavailableError{Err: err}
		}
		return nil, err
	}
	ref := storage.MediaReference{MediaID: id, OwnerType: "request", OwnerID: storage.NewID(), Purpose: "temporary", CreatedAt: m.options.Now()}
	// 存储原子拒绝对删除中对象添加引用，无需重复查询删除状态。
	if err := m.references.Add(ctx, &ref); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, &share.UnavailableError{Err: err}
		}
		return nil, err
	}
	return &heldMedia{manager: m, record: *item, reference: ref, mu: make(chan struct{}, 1)}, nil
}

// Info 返回持有记录的通用信息快照。
func (h *heldMedia) Info() share.MediaInfo { return mediaInfo(&h.record) }

// Open 更新访问事实并打开内容，不重复查询或添加引用。
func (h *heldMedia) Open(ctx context.Context) (io.ReadCloser, error) {
	if h.released.Load() {
		return nil, fmt.Errorf("media hold is closed")
	}
	backend, err := h.manager.backendForStoredMedia(&h.record)
	if err != nil {
		return nil, err
	}
	if err := h.manager.media.Touch(ctx, h.record.ID, h.manager.options.Now()); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, &share.UnavailableError{Err: err}
		}
		return nil, err
	}
	location := h.record.LocalPath
	if h.record.Backend == "s3" {
		location = h.record.ObjectKey
	}
	reader, err := backend.Open(ctx, location)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &share.UnavailableError{Err: err}
	}
	return reader, err
}

// Close 使用本轮清理上下文释放引用；等待可取消，失败保留引用。
func (h *heldMedia) Close(ctx context.Context) error {
	select {
	case h.mu <- struct{}{}:
		defer func() { <-h.mu }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.released.Load() {
		return nil
	}
	if err := h.manager.references.Remove(ctx, h.reference); err != nil {
		return err
	}
	h.released.Store(true)
	return nil
}

// releaseOwned 结束便捷方法独占的持有，失败后将最终释放责任交给 Cleanup。
// 交给 transfer 管理的 Hold 不经过此入口。
func (m *Manager) releaseOwned(held share.HeldMedia) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := held.Close(ctx)
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
		if err := held.Close(ctx); err != nil {
			remaining = append(remaining, held)
			if err != ctx.Err() {
				failures = append(failures, err)
			}
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

// Close 只收尾自有失败持有，不关闭借用存储，也不执行周期性回收。
func (m *Manager) Close(ctx context.Context) error { return m.retryReleases(ctx) }

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

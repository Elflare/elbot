package transfer

import (
	"context"
	"crypto/rand"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"elbot/internal/media/share"
)

// usage 保存一次使用的自有资源；消费者只持有其 ID。
type usage struct {
	mu    chan struct{}     // 串行清理资格，等待可取消。
	ended atomic.Bool       // 收到结束事实后，维护才可释放资源。
	held  []share.HeldMedia // 尚未成功释放的媒体持有。
	dir   string            // 尚未成功删除的私有目录。
}

func (m *Manager) registerUsage(held []share.HeldMedia, dir string) string {
	u := &usage{mu: make(chan struct{}, 1), held: held, dir: dir}
	id := rand.Text()
	m.usagesMu.Lock()
	m.usages[id] = u
	m.usagesMu.Unlock()
	return id
}

// FinishUsages 仅登记结束事实，不做文件或数据库操作。
func (m *Manager) FinishUsages(ids []string) {
	m.usagesMu.Lock()
	defer m.usagesMu.Unlock()
	for _, id := range ids {
		if u := m.usages[id]; u != nil {
			u.ended.Store(true)
		}
	}
}

// CleanupUsages 清理指定已结束记录；nil 参数表示维护当前集合快照。
// 资源失败单独记录并保留重试；返回 nil 仅表示遍历完成，中断返回 ctx.Err()。
func (m *Manager) CleanupUsages(ctx context.Context, ids []string) error {
	return m.cleanupUsages(ctx, ids, false)
}

// Close 仅处理已结束的 usage，首个资源清理错误直接返回。
func (m *Manager) Close(ctx context.Context) error { return m.cleanupUsages(ctx, nil, true) }

func (m *Manager) cleanupUsages(ctx context.Context, ids []string, stopOnError bool) error {
	m.usagesMu.Lock()
	entries := make(map[string]*usage)
	if ids == nil {
		for id, u := range m.usages {
			entries[id] = u
		}
	} else {
		for _, id := range ids {
			if u := m.usages[id]; u != nil {
				entries[id] = u
			}
		}
	}
	m.usagesMu.Unlock()
	for id, u := range entries {
		if err := m.cleanupUsage(ctx, id, u, stopOnError); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (m *Manager) cleanupUsage(ctx context.Context, id string, u *usage, stopOnError bool) error {
	select {
	case u.mu <- struct{}{}:
		defer func() { <-u.mu }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil || !u.ended.Load() {
		return err
	}
	if u.dir != "" {
		if err := os.RemoveAll(u.dir); err == nil {
			u.dir = ""
		} else if stopOnError {
			return err
		} else {
			m.options.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "media_usage_cleanup_failed", Module: "media", Content: fmt.Sprintf("usage_id=%s operation=remove_directory path=%s error=%v", id, u.dir, err), ContentLimit: 4096, ResultStatus: events.ResultFailed})
		}
	}
	held := u.held[:0]
	for i, item := range u.held {
		err := ctx.Err()
		if err == nil {
			err = item.Close(ctx)
			if err == nil {
				continue
			}
			if !stopOnError {
				held = append(held, item)
				if !errors.Is(err, ctx.Err()) {
					m.options.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "media_usage_cleanup_failed", Module: "media", Content: fmt.Sprintf("usage_id=%s operation=release_hold media_id=%s error=%v", id, item.Info().ID, err), ContentLimit: 4096, ResultStatus: events.ResultFailed})
				}
				continue
			}
		}
		u.held = append(held, u.held[i:]...)
		return err
	}
	u.held = held
	if u.dir == "" && len(held) == 0 {
		m.usagesMu.Lock()
		delete(m.usages, id)
		m.usagesMu.Unlock()
	}
	return ctx.Err()
}

// completeBatch 在准备退出时一次接管全部持有；失败则登记结束，清理失败的仍可重试。
func (m *Manager) completeBatch(ctx context.Context, held []share.HeldMedia, success bool) string {
	if len(held) == 0 {
		return ""
	}
	id := m.registerUsage(held, "")
	if success {
		return id
	}
	ids := []string{id}
	m.FinishUsages(ids)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := m.CleanupUsages(ctx, ids); err != nil {
		m.options.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "media_usage_cleanup_interrupted", Module: "media", Content: fmt.Sprintf("usage_id=%s error=%v", id, err), ContentLimit: 4096, ResultStatus: events.ResultCanceled})
	}
	return ""
}

// Hold 将显式持有登记到使用记录中，资源释放始终由 Media 负责。
func (m *Manager) Hold(ctx context.Context, id string) (operationresult.Result[share.HeldMedia], string, error) {
	held, err := m.objects.Hold(ctx, id)
	if err != nil {
		return operationresult.Result[share.HeldMedia]{}, "", err
	}
	if held.Status != operationresult.Success {
		return held, "", nil
	}
	return held, m.registerUsage([]share.HeldMedia{held.Data}, ""), nil
}

// Workspace 创建并登记调用私有目录；base 的路径范围由消费方决定。
func (m *Manager) Workspace(base string) (string, string, error) {
	dir, err := os.MkdirTemp(base, ".media-call-*")
	if err != nil {
		return "", "", err
	}
	return dir, m.registerUsage(nil, dir), nil
}

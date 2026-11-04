package transfer

import (
	"context"
	"crypto/rand"
	"elbot/internal/events"
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
	dirs  []string          // 尚未成功删除的私有目录。
}

func (m *Manager) registerUsage(held share.HeldMedia, dir string) string {
	u := &usage{mu: make(chan struct{}, 1)}
	if held != nil {
		u.held = append(u.held, held)
	}
	if dir != "" {
		u.dirs = append(u.dirs, dir)
	}
	id := rand.Text()
	m.usagesMu.Lock()
	m.usages[id] = u
	m.usagesMu.Unlock()
	return id
}

func (m *Manager) addHeld(id string, held share.HeldMedia) string {
	if id == "" {
		return m.registerUsage(held, "")
	}
	m.usagesMu.Lock()
	u := m.usages[id]
	m.usagesMu.Unlock()
	u.mu <- struct{}{}
	u.held = append(u.held, held)
	<-u.mu
	return id
}

// FinishUsages 仅登记结束事实，不做文件或数据库操作。
func (m *Manager) FinishUsages(ids []string) {
	for _, id := range ids {
		m.usagesMu.Lock()
		u := m.usages[id]
		m.usagesMu.Unlock()
		if u != nil {
			u.ended.Store(true)
		}
	}
}

// CleanupUsages 清理指定已结束记录；nil 参数表示维护当前集合快照。
// 资源失败单独记录并保留重试；返回 nil 仅表示遍历完成，中断返回 ctx.Err()。
func (m *Manager) CleanupUsages(ctx context.Context, ids []string) error {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case u.mu <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		if !u.ended.Load() {
			<-u.mu
			continue
		}
		dirs := u.dirs[:0]
		for _, dir := range u.dirs {
			if ctx.Err() != nil {
				dirs = append(dirs, dir)
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				dirs = append(dirs, dir)
				m.options.Log(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "media_usage_cleanup_failed", Module: "media", Content: fmt.Sprintf("usage_id=%s operation=remove_directory path=%s error=%v", id, dir, err), ContentLimit: 4096, ResultStatus: events.ResultFailed})
			}
		}
		u.dirs = dirs
		held := u.held[:0]
		for _, item := range u.held {
			if ctx.Err() != nil {
				held = append(held, item)
				continue
			}
			if err := item.Close(ctx); err != nil {
				held = append(held, item)
				if err != ctx.Err() {
					m.options.Log(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "media_usage_cleanup_failed", Module: "media", Content: fmt.Sprintf("usage_id=%s operation=release_hold media_id=%s error=%v", id, item.Info().ID, err), ContentLimit: 4096, ResultStatus: events.ResultFailed})
				}
			}
		}
		u.held = held
		if len(u.dirs) == 0 && len(u.held) == 0 {
			m.usagesMu.Lock()
			delete(m.usages, id)
			m.usagesMu.Unlock()
		}
		<-u.mu
	}
	return ctx.Err()
}

func (m *Manager) abortUsage(ctx context.Context, id string) {
	ids := []string{id}
	m.FinishUsages(ids)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := m.CleanupUsages(ctx, ids); err != nil {
		m.options.Log(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "media_usage_cleanup_interrupted", Module: "media", Content: fmt.Sprintf("usage_id=%s error=%v", id, err), ContentLimit: 4096, ResultStatus: events.ResultCanceled})
	}
}

// Hold 将显式持有登记到使用记录中，资源释放始终由 Media 负责。
func (m *Manager) Hold(ctx context.Context, id string) (share.HeldMedia, string, error) {
	held, err := m.objects.Hold(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return held, m.registerUsage(held, ""), nil
}

// Workspace 创建并登记调用私有目录；base 的路径范围由消费方决定。
func (m *Manager) Workspace(base string) (string, string, error) {
	dir, err := os.MkdirTemp(base, ".media-call-*")
	if err != nil {
		return "", "", err
	}
	return dir, m.registerUsage(nil, dir), nil
}

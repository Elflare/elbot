package object

import (
	"context"
	"sync"
)

// objectLocks 按对象身份协调变更，不阻塞不同 ID 的操作。
type objectLocks struct {
	mu      sync.Mutex             // 保护锁表和使用者计数。
	entries map[string]*objectLock // 活跃持有者及等待者共用的每 ID 锁。
}

// objectLock 的使用者计数同时覆盖持有者与等待者。
type objectLock struct {
	token chan struct{} // 单个对象的互斥资格。
	users int           // 持有及等待数量，归零后可从锁表删除。
}

// acquire 可取消地取得对象锁，并返回释放函数。
func (l *objectLocks) acquire(ctx context.Context, id string) (func(), error) { panic("TODO") }

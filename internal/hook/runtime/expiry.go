// 管理 KV 与媒体共用的到期索引；锁、定时器和通知归所属组件。
package runtime

import (
	"container/heap"
	"time"
)

type expiryEntry struct {
	key      string    // 所属集合中的唯一键。
	deadline time.Time // 下次到期或重试时间。
	index    int       // 当前堆位置，供续期和删除直接定位。
}

// expiryQueue 的所有访问由所属组件的集合锁保护；零值可用。
type expiryQueue struct {
	entries []*expiryEntry          // 按 deadline 排列的最小堆。
	byKey   map[string]*expiryEntry // 每个 key 最多一个调度项。
}

func (q *expiryQueue) Len() int           { return len(q.entries) }
func (q *expiryQueue) Less(i, j int) bool { return q.entries[i].deadline.Before(q.entries[j].deadline) }
func (q *expiryQueue) Swap(i, j int) {
	q.entries[i], q.entries[j] = q.entries[j], q.entries[i]
	q.entries[i].index, q.entries[j].index = i, j
}
func (q *expiryQueue) Push(value any) {
	entry := value.(*expiryEntry)
	entry.index = len(q.entries)
	q.entries = append(q.entries, entry)
	q.byKey[entry.key] = entry
}
func (q *expiryQueue) Pop() any {
	last := len(q.entries) - 1
	entry := q.entries[last]
	q.entries[last] = nil
	q.entries = q.entries[:last]
	delete(q.byKey, entry.key)
	return entry
}

// set 和 remove 返回最近期限是否变化，调用方据此唤醒自己的调度循环。
func (q *expiryQueue) set(key string, deadline time.Time) bool {
	previous := q.next()
	if entry := q.byKey[key]; entry != nil {
		entry.deadline = deadline
		heap.Fix(q, entry.index)
	} else {
		if q.byKey == nil {
			q.byKey = make(map[string]*expiryEntry)
		}
		heap.Push(q, &expiryEntry{key: key, deadline: deadline})
	}
	return !previous.Equal(q.next())
}

func (q *expiryQueue) remove(key string) bool {
	previous := q.next()
	if entry := q.byKey[key]; entry != nil {
		heap.Remove(q, entry.index)
	}
	return !previous.Equal(q.next())
}

func (q *expiryQueue) next() time.Time {
	if len(q.entries) == 0 {
		return time.Time{}
	}
	return q.entries[0].deadline
}

func (q *expiryQueue) popDue(now time.Time) (string, bool) {
	if len(q.entries) == 0 || q.entries[0].deadline.After(now) {
		return "", false
	}
	return heap.Pop(q).(*expiryEntry).key, true
}

package platform

import "time"

const (
	defaultBackoffBase = 3 * time.Second
	defaultBackoffMax  = 10 * time.Second
)

// Backoff 提供指数退避延迟，用于平台重连。
// 连接失败时 Delay() 逐次翻倍，封顶 max；连接成功后调用 Reset() 回到 base。
// 日志降级：ShouldWarn() 仅在状态从正常转为失败时返回 true，连续失败期间返回 false。
type Backoff struct {
	base   time.Duration
	max    time.Duration
	cur    time.Duration
	failed bool
}

func NewBackoff(base, max time.Duration) Backoff {
	if base <= 0 {
		base = defaultBackoffBase
	}
	if max <= 0 || max < base {
		max = defaultBackoffMax
	}
	return Backoff{base: base, max: max, cur: base}
}

// Delay 返回当前延迟并将下一次延迟翻倍（封顶 max）。
func (b *Backoff) Delay() time.Duration {
	d := b.cur
	next := b.cur * 2
	if next > b.max || next < b.cur {
		next = b.max
	}
	b.cur = next
	return d
}

// Reset 在连接成功后调用，延迟回到 base。
func (b *Backoff) Reset() {
	b.cur = b.base
	b.failed = false
}

// ShouldWarn 控制日志降级：首次失败返回 true 并标记 failed，
// 连续失败返回 false；Reset 后再次失败重新返回 true。
func (b *Backoff) ShouldWarn() bool {
	if b.failed {
		return false
	}
	b.failed = true
	return true
}

// 追加确认等待；状态修改由登记回调负责。
package confirmation

import (
	"context"
	"elbot/internal/turn/share"
	"time"
)

func WaitAppend(ctx context.Context, w *share.AppendWait, timeout time.Duration) bool {
	if w == nil {
		return false
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timerC = timer.C
		defer timer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			w.Cancel()
			return false
		case <-w.Done:
			return false
		case <-w.Refresh:
			if timer != nil {
				timer.Reset(timeout)
			}
		case <-timerC:
			return w.Cancel() && ctx.Err() == nil
		}
	}
}

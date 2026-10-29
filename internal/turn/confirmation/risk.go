// 风险确认等待与终态分类；不直接访问生命周期状态。
package confirmation

import (
	"context"
	"elbot/internal/turn/share"
	"time"
)

func WaitRisk(ctx context.Context, w *share.RiskWait, timeout time.Duration) share.RiskResult {
	if w == nil {
		return share.RiskResult{Outcome: share.RiskStale}
	}
	defer w.Cancel()
	if ctx.Err() != nil {
		return share.RiskResult{Outcome: share.RiskCanceled, Err: ctx.Err()}
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
		case decision := <-w.Response:
			return w.Complete(decision, ctx.Err())
		case <-w.Done:
			return w.Complete(share.RiskDecision{}, ctx.Err())
		case <-w.Refresh:
			if timer != nil {
				timer.Reset(timeout)
			}
		case <-ctx.Done():
			return share.RiskResult{Outcome: share.RiskCanceled, Err: ctx.Err()}
		case <-timerC:
			removed := w.Expire()
			if ctx.Err() != nil {
				return share.RiskResult{Outcome: share.RiskCanceled, Err: ctx.Err()}
			}
			if removed {
				return share.RiskResult{Outcome: share.RiskExpired}
			}
			timer.Reset(timeout)
		}
	}
}

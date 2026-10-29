// 构造 Turn 总管，并转接公开确认结果。
package turn

import (
	"context"
	"time"

	"elbot/internal/events"
)

func NewSupervisor() *Supervisor {
	return &Supervisor{turns: map[string]*state{}}
}

func (w *RiskConfirmationWait) Wait(ctx context.Context, timeout time.Duration) RiskResult {
	resp, ok, status := w.wait(ctx, timeout)
	result := RiskResult{Outcome: RiskStale}
	switch {
	case ok:
		result.Outcome = RiskAnswered
		result.Decision = RiskDecision{Action: RiskConfirm, Extra: resp.Extra, Reason: resp.Reason}
		switch {
		case resp.Rejected:
			result.Decision.Action = RiskReject
		case resp.ConfirmAll:
			result.Decision.Action = RiskConfirmAll
		case resp.ConfirmTool:
			result.Decision.Action = RiskConfirmTool
		}
	case resp.Expired:
		result.Outcome = RiskExpired
	case status == events.ResultCanceled || status == events.ResultFailed:
		result.Outcome, result.Err = RiskCanceled, ctx.Err()
	}
	return result
}

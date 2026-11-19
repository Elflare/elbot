package agent

import (
	"context"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (a controlAdapter) admitAnswers(ctx context.Context, row *storage.Session, batch *storage.AnswerBatch) (operationresult.Result[control.ConfirmAnswersResult], error) {
	var res operationresult.Result[control.ConfirmAnswersResult]
	m := a.supervisor
	if row.ArchivedAt != nil {
		res.Status = operationresult.Rejected
		res.Message = "会话已归档"
		return res, nil
	}
	binding, _ := session.BindingFromContext(ctx)
	for {
		snapshot := m.turns.Snapshot(row.ID)
		e := snapshot.Execution
		idle := snapshot.Phase == turn.PhaseIdle
		if idle {
			var err error
			e, err = m.turns.NewExecution(ctx)
			if err != nil {
				return res, err
			}
		} else if e == nil || snapshot.Phase == turn.PhaseStopping || snapshot.Phase == turn.PhaseCompact {
			res.Status = operationresult.Busy
			res.Message = "会话忙"
			return res, nil
		}
		batch.Bind(e.Done())
		input := turn.Input{Answers: []*storage.AnswerBatch{batch}}
		if idle {
			if !m.turns.ReserveExecution(row.ID, input, e) {
				continue
			}
			cancel := func() { m.turns.ReleaseReservation(row.ID, e) }
			run := func(next context.Context) (context.Context, operationresult.Result[struct{}], error) {
				defer cancel()
				select {
				case <-e.Done():
					return next, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "提交已取消"}, nil
				default:
				}
				return m.execution.RunAnswers(session.WithBinding(next, binding), row, input, e)
			}
			res.Data.Continuation = &control.Continuation{Kind: "answers", SessionID: row.ID, Answers: &control.AnswerSubmission{Run: run, Cancel: cancel}}
		} else if !m.turns.AppendPendingInput(row.ID, input) {
			continue
		}
		res.Status = operationresult.Success
		res.Data.QuestionIDs = batch.IDs
		return res, nil
	}
}

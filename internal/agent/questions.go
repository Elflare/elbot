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
	m := a.supervisor
	input := turn.Input{Answers: []*storage.AnswerBatch{batch}}
	admitted, err := m.execution.AdmitDeferredInput(ctx, row, input)
	res := operationresult.Result[control.ConfirmAnswersResult]{Status: admitted.Status, Message: admitted.Message}
	if err != nil || admitted.Status != operationresult.Success {
		return res, err
	}
	res.Data.QuestionIDs = batch.IDs
	if e := admitted.Data; e != nil {
		binding, _ := session.BindingFromContext(ctx)
		cancel := func() { m.turns.ReleaseReservation(row.ID, e) }
		run := func(next context.Context) (context.Context, operationresult.Result[struct{}], error) {
			defer cancel()
			return m.execution.RunDeferredInput(session.WithBinding(next, binding), row, input, e)
		}
		res.Data.Continuation = &control.Continuation{Kind: "answers", SessionID: row.ID, Answers: &control.AnswerSubmission{Run: run, Cancel: cancel}}
	}
	return res, nil
}

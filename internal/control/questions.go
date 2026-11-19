package control

import (
	"context"
	"elbot/internal/control/share"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Question = storage.Question
type QuestionDraft = storage.QuestionDraft
type ConfirmAnswersResult struct {
	QuestionIDs  []int64       `json:"question_ids"` // Questions included in this submission.
	Continuation *Continuation `json:"-"`
}

type AdmitAnswersFunc func(context.Context, *storage.Session, *storage.AnswerBatch) (operationresult.Result[ConfirmAnswersResult], error)

func (m *Supervisor) CreateQuestions(items []storage.Question) { m.questions.Create(items) }

// Slash and HTTP operations resolve the same activation and serialize edits with submission.
func withQuestionSession[T any](ctx context.Context, m *Supervisor, id string, do func(context.Context, *storage.Session) (operationresult.Result[T], error)) (operationresult.Result[T], error) {
	auth, err := share.Authorize(ctx, false)
	if err != nil || auth.Status != operationresult.Success {
		return operationresult.Result[T]{Status: auth.Status, Message: auth.Message}, err
	}
	current, err := m.deps.Sessions.CurrentBound(ctx, m.deps.Scope(ctx))
	if err != nil || current.Status != operationresult.Success {
		return operationresult.Result[T]{Status: current.Status, Message: current.Message}, err
	}
	b := current.Data.Binding
	original, ok := session.BindingFromContext(ctx)
	if (id != "" && b.SessionID() != id) || (ok && original != b) {
		return operationresult.Result[T]{Status: operationresult.Conflict, Message: "当前会话已切换"}, nil
	}
	locked, release, err := m.deps.Sessions.EnterBinding(ctx, b)
	if err != nil {
		if !b.Valid() {
			return operationresult.Result[T]{Status: operationresult.Conflict, Message: "当前会话已切换"}, nil
		}
		return operationresult.Result[T]{}, err
	}
	defer release()
	return do(session.WithBinding(locked, b), current.Data.Session)
}

func (m *Supervisor) ListQuestions(ctx context.Context, id string) (operationresult.Result[[]Question], error) {
	return withQuestionSession(ctx, m, id, func(ctx context.Context, row *storage.Session) (operationresult.Result[[]Question], error) {
		items := m.questions.List(row.ID)
		return operationresult.Result[[]Question]{Status: operationresult.Success, Data: items}, nil
	})
}

func (m *Supervisor) SaveQuestionDraft(ctx context.Context, id string, number int64, draft QuestionDraft) (operationresult.Result[Question], error) {
	return withQuestionSession(ctx, m, id, func(ctx context.Context, row *storage.Session) (operationresult.Result[Question], error) {
		return m.questions.Save(row.ID, number, draft), nil
	})
}

func (m *Supervisor) ConfirmAnswers(ctx context.Context, id string) (operationresult.Result[ConfirmAnswersResult], error) {
	return withQuestionSession(ctx, m, id, func(ctx context.Context, row *storage.Session) (operationresult.Result[ConfirmAnswersResult], error) {
		batch := m.questions.Claim(row.ID)
		if len(batch.IDs) == 0 {
			return operationresult.Result[ConfirmAnswersResult]{Status: operationresult.Rejected, Message: "无新草稿"}, nil
		}
		r, err := m.deps.AdmitAnswers(ctx, row, batch)
		if err != nil || r.Status != operationresult.Success {
			batch.Finish(false)
		}
		return r, err
	})
}

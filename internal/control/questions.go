package control

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Question = storage.Question
type QuestionDraft = storage.QuestionDraft
type ConfirmAnswersResult struct {
	QuestionIDs []int64 `json:"question_ids"` // Questions included in this submission.
}

func (m *Supervisor) CreateQuestions(items []storage.Question) *storage.QuestionGroup {
	return m.questions.Create(items)
}

// Slash and HTTP operations resolve the same activation and serialize edits with submission.
func withQuestionSession[T any](ctx context.Context, m *Supervisor, id string, do func(string) (operationresult.Result[T], error)) (operationresult.Result[T], error) {
	auth, err := authorize(ctx, false)
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
	_, release, err := m.deps.Sessions.EnterBinding(ctx, b)
	if err != nil {
		if !b.Valid() {
			return operationresult.Result[T]{Status: operationresult.Conflict, Message: "当前会话已切换"}, nil
		}
		return operationresult.Result[T]{}, err
	}
	defer release()
	return do(b.SessionID())
}

func (m *Supervisor) ListQuestions(ctx context.Context, id string) (operationresult.Result[[]Question], error) {
	return withQuestionSession(ctx, m, id, func(sid string) (operationresult.Result[[]Question], error) {
		items := m.questions.List(sid)
		return operationresult.Result[[]Question]{Status: operationresult.Success, Data: items}, nil
	})
}

func (m *Supervisor) SaveQuestionDraft(ctx context.Context, id string, number int64, draft QuestionDraft) (operationresult.Result[Question], error) {
	return withQuestionSession(ctx, m, id, func(sid string) (operationresult.Result[Question], error) {
		return m.questions.Save(sid, number, draft), nil
	})
}

func (m *Supervisor) ConfirmAnswers(ctx context.Context, id string) (operationresult.Result[ConfirmAnswersResult], error) {
	return withQuestionSession(ctx, m, id, func(sid string) (operationresult.Result[ConfirmAnswersResult], error) {
		ids := m.questions.Submit(sid)
		if len(ids) == 0 {
			return operationresult.Result[ConfirmAnswersResult]{Status: operationresult.Rejected, Message: "无待答题"}, nil
		}
		return operationresult.Result[ConfirmAnswersResult]{Status: operationresult.Success, Data: ConfirmAnswersResult{QuestionIDs: ids}}, nil
	})
}

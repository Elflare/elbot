package fileops

import (
	"context"

	"elbot/internal/operationresult"
)

// Service shares path policy and memory backups between tools and commands.
// Session binding is supplied by Agent, not by model arguments.
type Service struct {
	manager    *RollbackManager
	CheckWrite func(string) operationresult.Result[struct{}]
}

func NewService(checkWrite func(string) operationresult.Result[struct{}]) *Service {
	return &Service{manager: NewRollbackManager(), CheckWrite: checkWrite}
}

// Forget releases the backups owned by this binding.
func (s *Service) Forget(binding Binding) { s.manager.Forget(binding) }

func (s *Service) List(ctx context.Context) operationresult.Result[[]RollbackInfo] {
	binding := s.rollbackBinding(ctx)
	if binding.Status != operationresult.Success {
		return operationresult.Result[[]RollbackInfo]{Status: binding.Status, Message: binding.Message}
	}
	return binding.Data.session.List()
}

// RollbackByID resolves a human-facing backup number through the same lease and
// commit checks as tool rollback. Failed records retain their path for auditing.
func (s *Service) RollbackByID(ctx context.Context, id uint64) (operationresult.Result[RollbackResult], error) {
	records := s.List(ctx)
	if records.Status != operationresult.Success {
		return operationresult.Result[RollbackResult]{Status: records.Status, Message: records.Message}, nil
	}
	for _, record := range records.Data {
		if record.ID == id {
			return s.Rollback(ctx, record.Path, id)
		}
	}
	return operationresult.Result[RollbackResult]{Status: operationresult.NotFound, Message: "no rollback record: it may have been consumed, replaced, or evicted"}, nil
}

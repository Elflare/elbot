package fileops

import "context"

// Service shares path policy and memory backups between tools and commands.
// Session binding is supplied by Agent, not by model arguments.
type Service struct {
	Manager    *RollbackManager
	CheckWrite func(string) error
}

func NewService(checkWrite func(string) error) *Service {
	return &Service{Manager: NewRollbackManager(), CheckWrite: checkWrite}
}

func (s *Service) List(ctx context.Context) ([]RollbackInfo, error) {
	binding, err := s.rollbackBinding(ctx)
	if err != nil {
		return nil, err
	}
	return binding.session.List()
}

// RollbackByID resolves a human-facing backup number through the same lease and
// commit checks as tool rollback. Failed records retain their path for auditing.
func (s *Service) RollbackByID(ctx context.Context, id uint64) (RollbackResult, error) {
	records, err := s.List(ctx)
	if err != nil {
		return RollbackResult{}, err
	}
	for _, record := range records {
		if record.ID == id {
			result, err := s.Rollback(ctx, record.Path, id)
			if err != nil {
				return RollbackResult{Path: record.Path}, err
			}
			return result, nil
		}
	}
	return RollbackResult{}, ErrRollbackNotFound
}

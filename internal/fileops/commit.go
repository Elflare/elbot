package fileops

import (
	"context"

	"elbot/internal/operationresult"
)

// CommitAdmission is supplied by execution orchestration. File services never
// discover or replace the caller's Session binding.
type CommitAdmission func(context.Context) (operationresult.Result[CommitAdmissionData], error)

type CommitAdmissionData struct {
	Context context.Context
	Release func()
}

type commitGuard struct {
	// admission acquires the original caller's commit lease.
	admission CommitAdmission
	// validate rechecks resolved paths while commit admission is held.
	validate func(context.Context) (operationresult.Result[struct{}], error)
}

func (g commitGuard) enter(ctx context.Context) (operationresult.Result[CommitAdmissionData], error) {
	admitted := operationresult.Result[CommitAdmissionData]{Status: operationresult.Success, Data: CommitAdmissionData{Context: ctx, Release: func() {}}}
	if g.admission != nil {
		var err error
		admitted, err = g.admission(ctx)
		if err != nil {
			return operationresult.Result[CommitAdmissionData]{}, err
		}
		if admitted.Status != operationresult.Success {
			return admitted, nil
		}
	}
	if err := ctx.Err(); err != nil {
		admitted.Data.Release()
		return operationresult.Result[CommitAdmissionData]{}, err
	}
	if g.validate != nil {
		result, err := g.validate(admitted.Data.Context)
		if err != nil {
			admitted.Data.Release()
			return operationresult.Result[CommitAdmissionData]{}, err
		}
		if result.Status != operationresult.Success {
			admitted.Data.Release()
			return operationresult.Result[CommitAdmissionData]{Status: result.Status, Message: result.Message}, nil
		}
	}
	return admitted, nil
}

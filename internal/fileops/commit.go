package fileops

import "context"

// CommitAdmission is supplied by execution orchestration. File services never
// discover or replace the caller's Session binding.
type CommitAdmission func(context.Context) (context.Context, func(), error)

type commitGuard struct {
	// admission acquires the original caller's commit lease.
	admission CommitAdmission
	// validate rechecks resolved paths while commit admission is held.
	validate func(context.Context) error
}

func (g commitGuard) enter(ctx context.Context) (context.Context, func(), error) {
	locked, release := ctx, func() {}
	if g.admission != nil {
		var err error
		locked, release, err = g.admission(ctx)
		if err != nil {
			return ctx, nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		release()
		return ctx, nil, err
	}
	if g.validate != nil {
		if err := g.validate(locked); err != nil {
			release()
			return ctx, nil, err
		}
	}
	return locked, release, nil
}

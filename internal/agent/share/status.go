package share

import (
	"context"

	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
)

type ExecutionStatus interface {
	Record(context.Context, runtimestatus.Snapshot)
	Snapshot(string) runtimestatus.Snapshot
	Usage(*storage.Session) (*llm.Usage, error)
}

package contextmgr

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type Compactor interface {
	Prepare(context.Context, *storage.Session, string, modelmgr.Selection) (*PreparedCompact, error)
}
type CompactorResolver interface {
	CompactorFor(llm.Origin) (Compactor, error)
}

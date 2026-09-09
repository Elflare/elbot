package hook

import (
	"context"
	"io"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/storage"
)

// MediaAPI is the host-only media capability. It is never serialized to hook.v2.
// Go hooks share the center rather than accessing its storage implementation.
type MediaAPI interface {
	ImportURL(context.Context, string, media.Input) (*storage.Media, error)
	ImportFile(context.Context, string, media.Input) (*storage.Media, error)
	ImportReader(context.Context, io.Reader, int64, media.Input) (*storage.Media, error)
	ImportBytes(context.Context, []byte, media.Input) (*storage.Media, error)
	Open(context.Context, string) (io.ReadCloser, *storage.Media, error)
	Read(context.Context, string) ([]byte, *storage.Media, error)
	Export(context.Context, string, string) (*storage.Media, error)
	Metadata(context.Context, string) (*storage.Media, error)
	ResolveForLLM(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, error)
	ResolveForOutput(context.Context, string) (delivery.Source, func(), error)
	AddReference(context.Context, *storage.MediaReference) error
	RemoveReference(context.Context, storage.MediaReference) error
}

var _ MediaAPI = (*media.Manager)(nil)

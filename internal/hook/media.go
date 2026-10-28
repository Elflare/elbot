// 声明 Hook 对媒体中心的消费契约。

package hook

import (
	"context"
	"io"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
)

// MediaAPI is the host-only media capability. It is never serialized to hook.v2.
// Go hooks share the center rather than accessing its storage implementation.
type MediaAPI interface {
	ImportURL(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportFile(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportReader(context.Context, io.Reader, int64, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportBytes(context.Context, []byte, media.Input, media.ImportLimits) (media.MediaInfo, error)
	Open(context.Context, string) (io.ReadCloser, media.MediaInfo, media.UsageID, error)
	Read(context.Context, string) ([]byte, media.MediaInfo, error)
	Export(context.Context, string, string) (media.MediaInfo, error)
	Info(context.Context, string) (media.MediaInfo, error)
	AcquireForLLM(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, media.UsageID, error)
	ResolveForOutput(context.Context, string) (delivery.Source, media.UsageID, error)
	PublishUsageFinished(context.Context, media.UsageFinishedEvent)
	AddReference(context.Context, media.Reference) error
	RemoveReference(context.Context, media.Reference) error
}

var _ MediaAPI = (*media.MediaCenter)(nil)

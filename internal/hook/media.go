// 声明 Hook 对媒体中心的消费契约。

package hook

import (
	"context"
	"io"

	"elbot/internal/media"
)

// MediaAPI is the host-only media capability. It is never serialized to hook.v2.
// Go hooks share the center rather than accessing its storage implementation.
type MediaAPI interface {
	ImportURL(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportFile(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportReader(context.Context, io.Reader, int64, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportBytes(context.Context, []byte, media.Input, media.ImportLimits) (media.MediaInfo, error)
	Read(context.Context, string) ([]byte, media.MediaInfo, error)
	Export(context.Context, string, string) (media.MediaInfo, error)
	Info(context.Context, string) (media.MediaInfo, error)
	AddReference(context.Context, media.Reference) error
	RemoveReference(context.Context, media.Reference) error
}

var _ MediaAPI = (*media.MediaCenter)(nil)

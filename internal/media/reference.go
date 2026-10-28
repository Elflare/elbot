package media

import (
	"context"
	"elbot/internal/media/object"
	"elbot/internal/media/share"
	"elbot/internal/signal"
	"encoding/json"
	"io"
	"sort"
)

// Reference 只表达持久引用的业务身份，时间及存储字段归对象管理。
type Reference struct {
	MediaID   string // 被引用媒体。
	OwnerType string // 引用所属业务类别。
	OwnerID   string // 引用所属业务对象。
	Purpose   string // 引用用途。
}

// HeldMedia 是单个媒体的公开持有句柄，不公开内部签名能力。
// 调用方先关闭读取器，再发布 UsageID 使用结束；Media 负责释放持有。
type HeldMedia struct {
	held    share.HeldMedia // 内部持有凭据，不向调用方开放。
	UsageID UsageID         // 本次持有对应的独立使用。
}

// Hold 为单个媒体取得临时引用，与业务持久引用独立。
func (m *MediaCenter) Hold(ctx context.Context, id string) (*HeldMedia, error) {
	if m.closed.Load() {
		return nil, signal.ErrClosed
	}
	held, usageID, err := m.transfer.Hold(ctx, id)
	if held == nil {
		return nil, err
	}
	return &HeldMedia{held: held, UsageID: UsageID(usageID)}, err
}

// Info 返回已持有媒体的通用信息快照。
func (h *HeldMedia) Info() MediaInfo { return MediaInfo(h.held.Info()) }

// Open 在已有持有下打开内容，不重新查询或增加引用。
func (h *HeldMedia) Open(ctx context.Context) (io.ReadCloser, error) { return h.held.Open(ctx) }

// AddReference 添加持久业务引用，不承担 Agent 的会话提交事务。
func (m *MediaCenter) AddReference(ctx context.Context, reference Reference) error {
	return m.objects.AddReference(ctx, object.Reference(reference))
}

// RemoveReference 删除指定业务身份的引用。
func (m *MediaCenter) RemoveReference(ctx context.Context, reference Reference) error {
	return m.objects.RemoveReference(ctx, object.Reference(reference))
}

// ArgumentMediaIDs 提取 JSON 参数中完整的媒体标识值，去重并排序。
func ArgumentMediaIDs(arguments string) []string {
	var value any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case string:
			if ValidID(value) {
				seen[value] = true
			}
		case []any:
			for _, item := range value {
				visit(item)
			}
		case map[string]any:
			for _, item := range value {
				visit(item)
			}
		}
	}
	visit(value)
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

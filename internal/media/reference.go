package media

import (
	"context"
	"elbot/internal/media/share"
	"io"
)

// Reference 只表达持久引用的业务身份，时间及存储字段归对象管理。
type Reference struct {
	MediaID   string // 被引用媒体。
	OwnerType string // 引用所属业务类别。
	OwnerID   string // 引用所属业务对象。
	Purpose   string // 引用用途。
}

// HeldMedia 是单个媒体的公开持有句柄，不公开内部签名能力。
// 调用方先关闭读取器，再关闭句柄；同一调用的复用集合由调用方管理。
type HeldMedia struct {
	held share.HeldMedia // 内部持有凭据，不向调用方开放。
}

// Hold 为单个媒体取得临时引用，与业务持久引用独立。
func (m *MediaCenter) Hold(ctx context.Context, id string) (*HeldMedia, error) { panic("TODO") }

// Info 返回已持有媒体的通用信息快照。
func (h *HeldMedia) Info() MediaInfo { panic("TODO") }

// Open 在已有持有下打开内容，不重新查询或增加引用。
func (h *HeldMedia) Open(ctx context.Context) (io.ReadCloser, error) { panic("TODO") }

// Close 释放持有；失败保留凭据供重试，成功后重复调用不重复释放。
func (h *HeldMedia) Close() error { panic("TODO") }

// AddReference 添加持久业务引用，不承担 Agent 的会话提交事务。
func (m *MediaCenter) AddReference(ctx context.Context, reference Reference) error { panic("TODO") }

// RemoveReference 删除指定业务身份的引用。
func (m *MediaCenter) RemoveReference(ctx context.Context, reference Reference) error { panic("TODO") }

// ArgumentMediaIDs 提取 JSON 参数中完整的媒体标识值，去重并排序。
func ArgumentMediaIDs(arguments string) []string { panic("TODO") }

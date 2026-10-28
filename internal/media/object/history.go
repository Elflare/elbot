package object

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/media/share"
	"elbot/internal/storage"
)

// HistoryIDs 合并有效历史关联和输出缓存，只返回序号与媒体标识。
func (m *Manager) HistoryIDs(ctx context.Context, key share.HistoryKey) (map[int]string, error) {
	entries, err := m.LoadHistoryMedia(ctx, key)
	if err != nil {
		return nil, err
	}
	ids := make(map[int]string, len(entries))
	for index, entry := range entries {
		ids[index] = entry.Media.ID
	}
	return ids, nil
}

// LoadHistoryMedia 按媒体 ID 去重读取，合并关联与有效输出缓存。
// 只返回通用信息和匹配关联状态；删除中记录不会成为可用缓存。
func (m *Manager) LoadHistoryMedia(ctx context.Context, key share.HistoryKey) (map[int]share.HistoryEntry, error) {
	entries := map[int]share.HistoryEntry{}
	mediaByID := map[string]share.MediaInfo{}
	lookup := func(id string) (share.MediaInfo, error) {
		if item, ok := mediaByID[id]; ok {
			return item, nil
		}
		item, err := m.Info(ctx, id)
		if err != nil && !share.IsUnavailable(err) {
			return share.MediaInfo{}, err
		}
		mediaByID[id] = item
		return item, nil
	}
	associations, err := m.media.FindHistory(ctx, key.Platform, key.ScopeID, key.MessageID)
	if err != nil {
		return nil, err
	}
	for _, association := range associations {
		if association.HistoryID != key.ID {
			continue
		}
		item, err := lookup(association.MediaID)
		if err != nil {
			return nil, err
		}
		if item.ID != "" {
			entries[association.MediaIndex] = share.HistoryEntry{Media: item, Associated: true, Kind: association.Kind}
		}
	}
	outputs, err := m.media.FindOutputs(ctx, key.Platform, key.ScopeID, key.MessageID, m.options.Now())
	if err != nil {
		return nil, err
	}
	for _, output := range outputs {
		item, err := lookup(output.MediaID)
		if err != nil {
			return nil, err
		}
		if item.ID == "" {
			continue
		}
		index := output.SegmentIndex + 1
		entry := entries[index]
		if entry.Media.ID != item.ID {
			entry = share.HistoryEntry{}
		}
		entry.Media = item
		entries[index] = entry
	}
	return entries, nil
}

// AssociateHistory 保存给定历史身份、位置与媒体之间的持久关联。
func (m *Manager) AssociateHistory(ctx context.Context, key share.HistoryKey, index int, kind, id string) error {
	if !share.ValidID(id) {
		return fmt.Errorf("invalid media ID")
	}
	return m.media.SaveHistory(ctx, storage.HistoryMedia{HistoryID: key.ID, Platform: key.Platform, ScopeID: key.ScopeID, MessageID: key.MessageID, MediaIndex: index, Kind: kind, MediaID: id})
}

// ReconcileHistory 分页核对历史所有者，仅释放确认失效的关联。
// 查询不可用不是历史已删除的证据；history 不保存到组件。
func (m *Manager) ReconcileHistory(ctx context.Context, history History) error {
	if history == nil {
		return nil
	}
	const pageSize = 100
	after := ""
	owners := map[[3]string]string{}
	for {
		items, err := m.media.ListHistory(ctx, after, pageSize)
		if err != nil {
			return err
		}
		for _, item := range items {
			key := [3]string{item.Platform, item.ScopeID, item.MessageID}
			owner, known := owners[key]
			if !known {
				row, err := history.GetByPlatformMessage(ctx, item.Platform, item.ScopeID, item.MessageID)
				if err != nil && !errors.Is(err, storage.ErrNotFound) {
					return fmt.Errorf("check history media owner: %w", err)
				}
				if err == nil {
					owner = row.ID
				}
				owners[key] = owner
			}
			if owner != item.HistoryID {
				if err := m.media.DeleteHistory(ctx, item.OwnerID); err != nil {
					return err
				}
			}
		}
		if len(items) < pageSize {
			return nil
		}
		after = items[len(items)-1].OwnerID
	}
}

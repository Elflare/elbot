package media

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/storage"
)

// ImportPlatform resolves transport-only sources without persisting credentials or local paths.
func (m *Manager) ImportPlatform(ctx context.Context, platformName string, resolver func(context.Context, PlatformMedia, int64) (delivery.Source, error), segment PlatformMedia) (*storage.Media, error) {
	if segment.MediaID != "" {
		return m.Metadata(ctx, segment.MediaID)
	}
	if segment.Size > m.MaxImportBytes {
		return nil, fmt.Errorf("media exceeds import limit of %d bytes", m.MaxImportBytes)
	}
	source := delivery.Source{URL: strings.TrimSpace(segment.URL)}
	var err error
	if source.URL == "" && resolver != nil {
		source, err = resolver(ctx, segment, m.MaxImportBytes)
	}
	if err != nil {
		return nil, err
	}
	input := Input{Name: segment.Name, MIMEType: segment.MIMEType, Source: Source{Platform: platformName, URL: segment.URL, FileID: segment.PlatformFileID}}
	if input.MIMEType == "" {
		input.MIMEType = source.MIMEType
	}
	switch {
	case source.MediaID != "":
		return m.Metadata(ctx, source.MediaID)
	case source.URL != "":
		return m.ImportURL(ctx, source.URL, input)
	case source.Path != "":
		return m.ImportFile(ctx, source.Path, input)
	case len(source.Data) > 0:
		return m.ImportBytes(ctx, source.Data, input)
	default:
		return nil, fmt.Errorf("platform media source unavailable")
	}
}

// HistoryIDs consults local associations only and never downloads a source.
func (m *Manager) HistoryIDs(ctx context.Context, row HistoryMessage) (map[int]string, error) {
	entries, err := m.loadHistoryMedia(ctx, row)
	if err != nil {
		return nil, err
	}
	ids := map[int]string{}
	for index, entry := range entries {
		ids[index] = entry.media.ID
	}
	return ids, nil
}

type historyMediaEntry struct {
	// media 是本次查询已读取且可用的媒体记录，供批量获取直接复用。
	media *storage.Media
	// association 仅在历史行身份和当前媒体匹配时保留；输出缓存单独命中时为 nil。
	association *storage.HistoryMedia
}

// loadHistoryMedia 合并历史关联与有效输出缓存，按媒体 ID 去重读取，并保留匹配的持久关联以跳过重复保存。
func (m *Manager) loadHistoryMedia(ctx context.Context, row HistoryMessage) (map[int]historyMediaEntry, error) {
	entries := map[int]historyMediaEntry{}
	mediaByID := map[string]*storage.Media{}
	lookup := func(id string) (*storage.Media, error) {
		if item, ok := mediaByID[id]; ok {
			return item, nil
		}
		item, err := m.Metadata(ctx, id)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}
		mediaByID[id] = item
		return item, nil
	}
	associations, err := m.Store.Media().FindHistory(ctx, row.Platform, row.ScopeID, row.MessageID)
	if err != nil {
		return nil, err
	}
	for _, association := range associations {
		if association.HistoryID != row.ID {
			continue
		}
		item, err := lookup(association.MediaID)
		if err != nil {
			return nil, err
		}
		if item != nil && !item.Deleting {
			entries[association.MediaIndex] = historyMediaEntry{media: item, association: &association}
		}
	}
	outputs, err := m.Store.Media().FindOutputs(ctx, row.Platform, row.ScopeID, row.MessageID, m.Now())
	if err != nil {
		return nil, err
	}
	for _, output := range outputs {
		item, err := lookup(output.MediaID)
		if err != nil {
			return nil, err
		}
		if item != nil && !item.Deleting {
			index := output.SegmentIndex + 1
			entry := entries[index]
			if entry.media == nil || entry.media.ID != item.ID {
				entry.association = nil
			}
			entry.media = item
			entries[index] = entry
		}
	}
	return entries, nil
}

func (m *Manager) AssociateHistory(ctx context.Context, row HistoryMessage, index int, kind string, id string) error {
	return m.Store.Media().SaveHistory(ctx, storage.HistoryMedia{HistoryID: row.ID, Platform: row.Platform, ScopeID: row.ScopeID, MessageID: row.MessageID, MediaIndex: index, Kind: kind, MediaID: id})
}

func (m *Manager) GetHistoryMedia(ctx context.Context, row HistoryMessage, index int, resolver func(context.Context, string, PlatformMedia, int64) (delivery.Source, error)) (*storage.Media, error) {
	results, err := m.GetHistoryMediaBatch(ctx, []HistoryMediaRequest{{Message: row, Index: index}}, resolver, HistoryFetchOptions{})
	if err != nil {
		return nil, err
	}
	return results[0].Media, results[0].Err
}

var ErrHistoryFetchLimit = errors.New("history media fetch limit reached")
var ErrHistoryMediaIndex = errors.New("history media index out of range")

type HistoryMediaRequest struct {
	Message HistoryMessage // 目标历史消息及有序媒体。
	Index   int            // 从 1 开始的媒体序号。
}

type HistoryMediaResult struct {
	Media *storage.Media
	Err   error
}

type HistoryFetchOptions struct {
	// MaxFetchAttempts limits uncached positions, including failed attempts.
	// Nonpositive values are unlimited. Cached media never consumes the budget.
	MaxFetchAttempts int
}

// GetHistoryMediaBatch owns cache decisions and acquisition state for one call.
// Results retain input order, including duplicate positions and per-item failures.
func (m *Manager) GetHistoryMediaBatch(ctx context.Context, requests []HistoryMediaRequest, resolver func(context.Context, string, PlatformMedia, int64) (delivery.Source, error), opts HistoryFetchOptions) ([]HistoryMediaResult, error) {
	type messageKey struct{ platform, scope, message, history string }
	type position struct {
		messageKey
		index int
	}
	known := map[messageKey]map[int]historyMediaEntry{}
	seen := map[position]HistoryMediaResult{}
	results := make([]HistoryMediaResult, 0, len(requests))
	attempts := 0
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, index := request.Message, request.Index
		key := messageKey{row.Platform, row.ScopeID, row.MessageID, row.ID}
		pos := position{key, index}
		if result, ok := seen[pos]; ok {
			results = append(results, result)
			continue
		}
		var result HistoryMediaResult
		segments := row.Media
		if index < 1 || index > len(segments) {
			result.Err = fmt.Errorf("%w: %d (message has %d media)", ErrHistoryMediaIndex, index, len(segments))
		} else {
			entries, ok := known[key]
			if !ok {
				var err error
				entries, err = m.loadHistoryMedia(ctx, row)
				if err != nil {
					return nil, err
				}
				known[key] = entries
			}
			entry := entries[index]
			segment := segments[index-1]
			if entry.media != nil {
				result.Media = entry.media
			} else if opts.MaxFetchAttempts > 0 && attempts >= opts.MaxFetchAttempts {
				result.Err = ErrHistoryFetchLimit
			} else {
				attempts++
				segment.MediaID = ""
				var resolve func(context.Context, PlatformMedia, int64) (delivery.Source, error)
				if resolver != nil {
					resolve = func(ctx context.Context, input PlatformMedia, limit int64) (delivery.Source, error) {
						return resolver(ctx, row.Platform, input, limit)
					}
				}
				result.Media, result.Err = m.ImportPlatform(ctx, row.Platform, resolve, segment)
			}
			if result.Err == nil && (entry.association == nil || entry.association.Kind != segment.Type) {
				result.Err = m.AssociateHistory(ctx, row, index, segment.Type, result.Media.ID)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seen[pos] = result
		results = append(results, result)
	}
	return results, nil
}

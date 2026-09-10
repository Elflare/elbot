package media

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

// ImportPlatform resolves transport-only sources without persisting credentials or local paths.
func (m *Manager) ImportPlatform(ctx context.Context, platformName string, resolver platform.MediaResolver, segment platform.MessageSegment) (*storage.Media, error) {
	if segment.MediaID != "" {
		return m.Metadata(ctx, segment.MediaID)
	}
	if segment.Size > m.MaxImportBytes {
		return nil, fmt.Errorf("media exceeds import limit of %d bytes", m.MaxImportBytes)
	}
	source := delivery.Source{URL: strings.TrimSpace(segment.URL)}
	var err error
	if source.URL == "" && resolver != nil {
		source, err = resolver.ResolveMedia(ctx, segment, m.MaxImportBytes)
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

// HistorySegments returns only media positions; text never consumes a media index.
func HistorySegments(row storage.ChatMessage) []platform.MessageSegment {
	var out []platform.MessageSegment
	for _, segment := range platform.UnmarshalChatSegments(row.Segments) {
		if segment.Type == platform.SegmentImage || segment.Type == platform.SegmentFile {
			out = append(out, segment)
		}
	}
	return out
}

// HistoryIDs consults local associations only and never downloads a source.
func (m *Manager) HistoryIDs(ctx context.Context, row storage.ChatMessage) (map[int]string, error) {
	ids := map[int]string{}
	associations, err := m.Store.Media().FindHistory(ctx, row.Platform, row.PlatformScopeID, row.PlatformMessageID)
	if err != nil {
		return nil, err
	}
	for _, association := range associations {
		if association.HistoryID != row.ID {
			continue
		}
		if item, err := m.Metadata(ctx, association.MediaID); err == nil {
			if !item.Deleting {
				ids[association.MediaIndex] = association.MediaID
			}
		} else if err != storage.ErrNotFound {
			return nil, err
		}
	}
	outputs, err := m.Store.Media().FindOutputs(ctx, row.Platform, row.PlatformScopeID, row.PlatformMessageID, m.Now())
	if err != nil {
		return nil, err
	}
	for _, output := range outputs {
		if item, err := m.Metadata(ctx, output.MediaID); err == nil {
			if !item.Deleting {
				ids[output.SegmentIndex+1] = output.MediaID
			}
		} else if err != storage.ErrNotFound {
			return nil, err
		}
	}
	return ids, nil
}

func (m *Manager) AssociateHistory(ctx context.Context, row storage.ChatMessage, index int, kind platform.MessageSegmentType, id string) error {
	return m.Store.Media().SaveHistory(ctx, storage.HistoryMedia{HistoryID: row.ID, Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID, MediaIndex: index, Kind: string(kind), MediaID: id})
}

func (m *Manager) GetHistoryMedia(ctx context.Context, row storage.ChatMessage, index int, resolver platform.MediaResolver) (*storage.Media, error) {
	segments := HistorySegments(row)
	if index < 1 || index > len(segments) {
		return nil, fmt.Errorf("media index %d out of range (message has %d media)", index, len(segments))
	}
	ids, err := m.HistoryIDs(ctx, row)
	if err != nil {
		return nil, err
	}
	segment := segments[index-1]
	segment.MediaID = ids[index]
	item, err := m.ImportPlatform(ctx, row.Platform, resolver, segment)
	if err != nil {
		return nil, err
	}
	if err := m.AssociateHistory(ctx, row, index, segment.Type, item.ID); err != nil {
		return nil, err
	}
	return item, nil
}

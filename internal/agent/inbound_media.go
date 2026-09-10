package agent

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

func (a *Agent) materializePlatformMedia(ctx context.Context) context.Context {
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok || a.media == nil {
		return ctx
	}
	cache := map[platform.MessageSegment]platform.MessageSegment{}
	resolve := func(segments []platform.MessageSegment) []platform.MessageSegment {
		out := append([]platform.MessageSegment(nil), segments...)
		for i, segment := range out {
			if segment.Type != platform.SegmentImage && segment.Type != platform.SegmentFile {
				continue
			}
			if resolved, ok := cache[segment]; ok {
				out[i] = resolved
				continue
			}
			resolved := a.materializePlatformSegment(ctx, msg, segment)
			cache[segment] = resolved
			out[i] = resolved
		}
		return out
	}
	msg.Segments = resolve(msg.Segments)
	msg.ContextSegments = resolve(msg.ContextSegments)
	msg.Reply.Segments = resolve(msg.Reply.Segments)
	return platform.WithMessageContext(ctx, msg)
}

func (a *Agent) materializePlatformSegment(ctx context.Context, msg platform.MessageContext, segment platform.MessageSegment) platform.MessageSegment {
	if segment.MediaID != "" {
		item, err := a.media.Metadata(ctx, segment.MediaID)
		if err != nil {
			return unavailablePlatformSegment(segment)
		}
		segment.Name, segment.MIMEType, segment.Size = item.Name, item.MIMEType, item.Size
		segment.URL, segment.PlatformFileID = "", ""
		return segment
	}
	if segment.Size > a.media.MaxImportBytes {
		return unavailablePlatformSegment(segment)
	}
	source := delivery.Source{URL: strings.TrimSpace(segment.URL)}
	var err error
	if source.URL == "" && msg.MediaResolver != nil {
		source, err = msg.MediaResolver.ResolveMedia(ctx, segment, a.media.MaxImportBytes)
	}
	if err != nil {
		return unavailablePlatformSegment(segment)
	}
	input := media.Input{Name: segment.Name, MIMEType: segment.MIMEType, Source: media.Source{Platform: msg.Platform, URL: segment.URL, FileID: segment.PlatformFileID}}
	if input.MIMEType == "" {
		input.MIMEType = source.MIMEType
	}
	var item *storage.Media
	switch {
	case source.MediaID != "":
		item, err = a.media.Metadata(ctx, source.MediaID)
	case source.URL != "":
		item, err = a.media.ImportURL(ctx, source.URL, input)
	case source.Path != "":
		item, err = a.media.ImportFile(ctx, source.Path, input)
	case len(source.Data) > 0:
		item, err = a.media.ImportBytes(ctx, source.Data, input)
	default:
		err = fmt.Errorf("platform media source unavailable")
	}
	if err != nil || item == nil {
		return unavailablePlatformSegment(segment)
	}
	segment.MediaID = item.ID
	segment.Name = item.Name
	segment.MIMEType = item.MIMEType
	segment.Size = item.Size
	segment.URL = ""
	segment.PlatformFileID = ""
	return segment
}

func unavailablePlatformSegment(segment platform.MessageSegment) platform.MessageSegment {
	label := strings.TrimSpace(segment.Name)
	if label == "" {
		label = "未知媒体"
	}
	return platform.MessageSegment{Type: platform.SegmentText, Text: "[媒体不可用；" + label + "]"}
}

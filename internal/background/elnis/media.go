// 准备事件输入及报告附件；来源分类只发生在协议入口。
package elnis

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
)

// Media 是事件输入和报告附件需要的归档与引用能力。
type Media interface {
	Info(context.Context, string) (media.MediaInfo, error)
	ImportURL(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportReader(context.Context, io.Reader, int64, media.Input, media.ImportLimits) (media.MediaInfo, error)
	ImportBytes(context.Context, []byte, media.Input, media.ImportLimits) (media.MediaInfo, error)
	AddEventReference(ctx context.Context, mediaID, eventID, purpose string) error
}

// validateMediaSegment 校验协议并保留明确来源，不下载媒体。
func validateMediaSegment(seg Segment, maxBytes int64) (delivery.Output, error) {
	out := delivery.Output{Kind: delivery.Kind(seg.Kind), Text: seg.Text, Name: seg.Name, Source: delivery.Source{MIMEType: seg.MIMEType}}
	switch seg.Kind {
	case SegmentKindText:
		if seg.URL != "" {
			return delivery.Output{}, fmt.Errorf("text segment cannot contain media")
		}
		return out, nil
	case SegmentKindImage, SegmentKindFile:
	default:
		return delivery.Output{}, fmt.Errorf("unsupported segment kind %q", seg.Kind)
	}
	value := strings.TrimSpace(seg.URL)
	parsed, err := url.Parse(value)
	if err != nil {
		return delivery.Output{}, fmt.Errorf("invalid media URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "media":
		if !media.ValidID(value) {
			return delivery.Output{}, fmt.Errorf("invalid media ID")
		}
		out.Source.MediaID = value
	case "http", "https":
		if parsed.Host == "" {
			return delivery.Output{}, fmt.Errorf("media URL requires a host")
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		out.Source.URL = parsed.String()
	case "data":
		header, encoded, ok := strings.Cut(value, ",")
		if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
			return delivery.Output{}, fmt.Errorf("data URL must use base64 encoding")
		}
		mimeType, _, err := mime.ParseMediaType(header[len("data:") : len(header)-len(";base64")])
		if err != nil {
			return delivery.Output{}, fmt.Errorf("invalid data URL MIME type")
		}
		if seg.MIMEType != "" && !strings.EqualFold(seg.MIMEType, mimeType) {
			return delivery.Output{}, fmt.Errorf("mime_type does not match data URL")
		}
		if maxBytes > 0 && int64(base64.StdEncoding.DecodedLen(len(encoded))) > maxBytes+2 {
			return delivery.Output{}, fmt.Errorf("data URL exceeds media size limit")
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return delivery.Output{}, fmt.Errorf("decode data URL: %w", err)
		}
		out.Source.MIMEType, out.Source.Data = mimeType, data
	default:
		return delivery.Output{}, fmt.Errorf("media source must be a media ID, HTTP(S) URL or base64 data URL")
	}
	return out, nil
}

func (s *Service) materializeSegments(ctx context.Context, segments []delivery.Output, eventID string) ([]llm.MessageSegment, error) {
	out := make([]llm.MessageSegment, 0, len(segments))
	limits := media.ImportLimits{MaxImportBytes: s.cfg.Segment.MaxFileBytes, DownloadTimeout: time.Duration(s.cfg.Segment.DownloadTimeoutSecs) * time.Second}
	for i, seg := range segments {
		if seg.Kind == delivery.KindText {
			out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: seg.Text})
			continue
		}
		input := media.Input{Name: seg.Name, MIMEType: seg.Source.MIMEType}
		var info media.MediaInfo
		var err error
		switch {
		case seg.Source.MediaID != "":
			info, err = s.media.Info(ctx, seg.Source.MediaID)
		case seg.Source.URL != "":
			info, err = s.media.ImportURL(ctx, seg.Source.URL, input, limits)
		default:
			info, err = s.media.ImportBytes(ctx, seg.Source.Data, input, limits)
		}
		if err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}
		if err := s.media.AddEventReference(ctx, info.ID, eventID, "input"); err != nil {
			return nil, err
		}
		out = append(out, llm.MessageSegment{Type: llm.MessageSegmentType(seg.Kind), Text: seg.Text, MediaID: info.ID, Name: info.Name, MIMEType: info.MIMEType})
	}
	return out, nil
}

func directText(req Request) string {
	parts := []string{}
	if title := strings.TrimSpace(req.Title); title != "" {
		parts = append(parts, title)
	}
	parts = append(parts, strings.TrimSpace(req.Content))
	return strings.Join(parts, "\n")
}

func (s *Service) directMediaOutputs(ctx context.Context, event Event, eventID string) ([]delivery.Output, error) {
	segments, err := s.materializeSegments(ctx, event.Segments, eventID)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return []delivery.Output{delivery.Text(directText(event.Request))}, nil
	}
	outputs := make([]delivery.Output, 0, len(segments))
	for _, seg := range segments {
		if seg.Type == llm.SegmentText {
			outputs = append(outputs, delivery.Text(seg.Text))
			continue
		}
		outputs = append(outputs, delivery.Output{Kind: delivery.Kind(seg.Type), Name: seg.Name, Source: delivery.Source{MediaID: seg.MediaID, MIMEType: seg.MIMEType}})
	}
	return outputs, nil
}

func (s *Service) importReportSegments(ctx context.Context, segments []delivery.Output, eventID string, workspace string) ([]delivery.Output, error) {
	out := append([]delivery.Output(nil), segments...)
	limits := media.ImportLimits{MaxImportBytes: s.cfg.Segment.MaxFileBytes, DownloadTimeout: time.Duration(s.cfg.Segment.DownloadTimeoutSecs) * time.Second}
	for i, seg := range out {
		var info media.MediaInfo
		var err error
		input := media.Input{Name: seg.Name, MIMEType: seg.Source.MIMEType}
		switch {
		case seg.Source.MediaID != "":
			info, err = s.media.Info(ctx, seg.Source.MediaID)
		case seg.Source.URL != "":
			info, err = s.media.ImportURL(ctx, seg.Source.URL, input, limits)
		default:
			info, err = func() (info media.MediaInfo, err error) {
				root, err := os.OpenRoot(workspace)
				if err != nil {
					return info, err
				}
				defer func() { err = errors.Join(err, root.Close()) }()
				file, err := root.Open(seg.Source.Path)
				if err != nil {
					return info, fmt.Errorf("open report media: %w", err)
				}
				defer func() { err = errors.Join(err, file.Close()) }()
				stat, err := file.Stat()
				if err != nil {
					return info, err
				}
				if !stat.Mode().IsRegular() {
					return info, fmt.Errorf("report media must be a regular file")
				}
				if input.Name == "" {
					input.Name = filepath.Base(seg.Source.Path)
				}
				return s.media.ImportReader(ctx, file, stat.Size(), input, limits)
			}()
		}
		if err != nil {
			return nil, err
		}
		if err := s.media.AddEventReference(ctx, info.ID, eventID, "report"); err != nil {
			return nil, err
		}
		out[i].Source = delivery.Source{MediaID: info.ID, MIMEType: info.MIMEType}
		out[i].Name = info.Name
	}
	return out, nil
}

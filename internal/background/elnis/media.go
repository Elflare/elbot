package elnis

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/storage"
	workspacepath "elbot/internal/workspace"
)

// Media 是事件输入和报告附件需要的媒体能力；导入仅返回稳定媒体 ID。
type Media interface {
	MaterializeWithLimits(context.Context, []llm.MessageSegment, media.ImportLimits) ([]llm.MessageSegment, error)
	ImportURL(context.Context, string, media.Input) (string, error)
	ImportReader(context.Context, io.Reader, int64, media.Input) (string, error)
	AddEventReference(ctx context.Context, mediaID, eventID, purpose string) error
}

func validateMediaSegment(seg Segment) error {
	switch seg.Kind {
	case SegmentKindText:
		if seg.URL != "" {
			return fmt.Errorf("text segment cannot contain media")
		}
		return nil
	case SegmentKindImage, SegmentKindFile:
		if strings.HasPrefix(strings.ToLower(seg.URL), media.IDPrefix) {
			if !media.ValidID(seg.URL) {
				return fmt.Errorf("invalid media ID")
			}
			return nil
		}
		return validateSegmentURL(seg.URL)
	default:
		return fmt.Errorf("unsupported segment kind %q", seg.Kind)
	}
}

func (s *Service) materializeSegments(ctx context.Context, segments []Segment, eventID string) ([]llm.MessageSegment, error) {
	out := segmentsLLM(segments)
	if len(out) == 0 {
		return out, nil
	}
	for i, seg := range segments {
		if seg.Kind == SegmentKindText {
			continue
		}
		if media.ValidID(seg.URL) {
			out[i].MediaID = seg.URL
			out[i].URL = ""
		}
		if out[i].Name == "" && strings.HasPrefix(seg.URL, "data:") {
			out[i].Name = storage.NewID()
		}
		if strings.HasPrefix(seg.URL, "data:") && filepath.Ext(out[i].Name) == "" {
			header, _, _ := strings.Cut(seg.URL, ",")
			if exts, _ := mime.ExtensionsByType(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")); len(exts) > 0 {
				out[i].Name += exts[0]
			}
		}
		resolved, err := s.media.MaterializeWithLimits(ctx, []llm.MessageSegment{out[i]}, media.ImportLimits{
			MaxImportBytes:  s.cfg.Segment.MaxFileBytes,
			DownloadTimeout: time.Duration(s.cfg.Segment.DownloadTimeoutSecs) * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}
		if len(resolved) != 1 || resolved[0].MediaID == "" {
			return nil, fmt.Errorf("segment %d media unavailable", i)
		}
		out[i] = resolved[0]
		if err := s.media.AddEventReference(ctx, out[i].MediaID, eventID, "input"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) directMediaOutputs(ctx context.Context, event Event, eventID string) ([]delivery.Output, error) {
	segments, err := s.materializeSegments(ctx, event.Request.Segments, eventID)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return []delivery.Output{delivery.Text(elvena.DirectText(event.Request))}, nil
	}
	outputs := make([]delivery.Output, 0, len(segments))
	for _, seg := range segments {
		if seg.Type == llm.SegmentText {
			outputs = append(outputs, delivery.Text(seg.Text))
			continue
		}
		kind := delivery.KindFile
		if seg.Type == llm.SegmentImage {
			kind = delivery.KindImage
		}
		outputs = append(outputs, delivery.Output{Kind: kind, Name: seg.Name, Source: delivery.Source{MediaID: seg.MediaID, MIMEType: seg.MIMEType}})
	}
	return outputs, nil
}

func (s *Service) importReportSegments(ctx context.Context, segments []llm.MessageSegment, eventID string, workspace string) ([]llm.MessageSegment, error) {
	out := append([]llm.MessageSegment(nil), segments...)
	for i, seg := range out {
		if seg.Type != llm.SegmentImage && seg.Type != llm.SegmentFile {
			return nil, fmt.Errorf("unsupported report segment type %q", seg.Type)
		}
		if seg.MediaID != "" && seg.URL != "" {
			return nil, fmt.Errorf("report media sources are mutually exclusive")
		}
		if media.ValidID(seg.URL) {
			seg.MediaID = seg.URL
			seg.URL = ""
		}
		if seg.MediaID == "" {
			var err error
			input := media.Input{Name: seg.Name, MIMEType: seg.MIMEType}
			if delivery.IsHTTPMediaSource(seg.URL) {
				seg.MediaID, err = s.media.ImportURL(ctx, seg.URL, input)
			} else {
				path, pathErr := workspacepath.NormalizeRestrictedPath(seg.URL)
				if pathErr != nil {
					return nil, pathErr
				}
				// 每项导入结束即关闭文件及根目录句柄。
				seg.MediaID, err = func() (string, error) {
					root, err := os.OpenRoot(workspace)
					if err != nil {
						return "", err
					}
					defer root.Close()
					file, err := root.Open(path)
					if err != nil {
						return "", fmt.Errorf("open report media: %w", err)
					}
					defer file.Close()
					info, err := file.Stat()
					if err != nil {
						return "", err
					}
					if !info.Mode().IsRegular() {
						return "", fmt.Errorf("report media must be a regular file")
					}
					if input.Name == "" {
						input.Name = filepath.Base(path)
					}
					return s.media.ImportReader(ctx, file, info.Size(), input)
				}()
			}
			if err != nil {
				return nil, err
			}
		}
		if err := s.media.AddEventReference(ctx, seg.MediaID, eventID, "report"); err != nil {
			return nil, err
		}
		seg.URL = ""
		out[i] = seg
	}
	return out, nil
}

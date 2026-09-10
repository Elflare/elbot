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
	"elbot/internal/tool"
)

// ImportWorkspaceMedia is a host API, not an Elvena RPC.
func (s *Service) ImportWorkspaceMedia(ctx context.Context, elwispName, path string, input media.Input) (*storage.Media, error) {
	if !elwispNamePattern.MatchString(elwispName) {
		return nil, fmt.Errorf("invalid elwisp name")
	}
	call := (&tool.MediaRuntime{Center: s.media}).NewCall()
	defer call.Close()
	return call.Import(ctx, filepath.Join(s.sandboxRoot, "elnis", elwispName), path, input)
}

// ExportWorkspaceMedia creates a new file within this Elwisp's workspace.
func (s *Service) ExportWorkspaceMedia(ctx context.Context, elwispName, id, path string) error {
	if s.media == nil {
		return fmt.Errorf("media center is not configured")
	}
	if !elwispNamePattern.MatchString(elwispName) || !filepath.IsLocal(path) || strings.Contains(path, ":") {
		return fmt.Errorf("invalid workspace path")
	}
	for _, part := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		if part == ".." {
			return fmt.Errorf("workspace path contains parent traversal")
		}
	}
	if strings.HasPrefix(strings.ReplaceAll(path, "\\", "/"), "/") {
		return fmt.Errorf("workspace path must be relative")
	}
	workspace := filepath.Join(s.sandboxRoot, "elnis", elwispName)
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	reader, _, err := s.media.Open(ctx, id)
	if err != nil {
		return err
	}
	defer reader.Close()
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = root.Remove(path)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	return nil
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
		if s.media == nil {
			return nil, fmt.Errorf("elnis media center is not configured")
		}
		// Preserve Elnis receive limits without modifying the shared manager.
		center := *s.media
		if s.cfg.Segment.MaxFileBytes > 0 && s.cfg.Segment.MaxFileBytes < center.MaxImportBytes {
			center.MaxImportBytes = s.cfg.Segment.MaxFileBytes
		}
		if s.cfg.Segment.DownloadTimeoutSecs > 0 {
			center.DownloadTimeout = time.Duration(s.cfg.Segment.DownloadTimeoutSecs) * time.Second
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
		resolved := center.Materialize(ctx, []llm.MessageSegment{out[i]})
		if len(resolved) != 1 || resolved[0].MediaID == "" {
			return nil, fmt.Errorf("segment %d media unavailable", i)
		}
		out[i] = resolved[0]
		if err := s.media.AddReference(ctx, &storage.MediaReference{MediaID: out[i].MediaID, OwnerType: "elnis_event", OwnerID: eventID, Purpose: "input"}); err != nil {
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
	bridge := (&tool.MediaRuntime{Center: s.media}).NewCall()
	defer bridge.Close()
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
		if seg.MediaID != "" {
			if _, err := s.media.Metadata(ctx, seg.MediaID); err != nil {
				return nil, err
			}
		} else if delivery.IsHTTPMediaSource(seg.URL) {
			// Keep existing HTTP report compatibility while persisting only stable IDs.
			item, err := s.media.ImportURL(ctx, seg.URL, media.Input{Name: seg.Name, MIMEType: seg.MIMEType})
			if err != nil {
				return nil, err
			}
			seg.MediaID = item.ID
		} else {
			item, err := bridge.Import(ctx, workspace, seg.URL, media.Input{Name: seg.Name, MIMEType: seg.MIMEType})
			if err != nil {
				return nil, err
			}
			seg.MediaID = item.ID
		}
		if err := s.media.AddReference(ctx, &storage.MediaReference{MediaID: seg.MediaID, OwnerType: "elnis_event", OwnerID: eventID, Purpose: "report"}); err != nil {
			return nil, err
		}
		seg.URL = ""
		out[i] = seg
	}
	return out, nil
}

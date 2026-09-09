package skill

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/tool"
)

// prepareGoMedia changes only the stdin copy and only an explicit media_inputs field.
func prepareGoMedia(ctx context.Context, raw json.RawMessage, call *tool.MediaCall, base string) (json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	value, ok := payload["media_inputs"]
	if !ok {
		return raw, nil
	}
	var inputs []tool.MediaInput
	if err := json.Unmarshal(value, &inputs); err != nil {
		return nil, fmt.Errorf("media_inputs: %w", err)
	}
	type preparedInput struct {
		MediaID  string `json:"media_id"`
		Path     string `json:"path"`
		Name     string `json:"name"`
		MIMEType string `json:"mime_type"`
		Size     int64  `json:"size"`
		Base64   string `json:"base64,omitempty"`
	}
	prepared := make([]preparedInput, 0, len(inputs))
	for _, input := range inputs {
		path, metadata, err := call.Export(ctx, input.MediaID, base)
		if err != nil {
			return nil, err
		}
		entry := preparedInput{MediaID: input.MediaID, Path: filepath.ToSlash(path), Name: metadata.Name, MIMEType: metadata.MIMEType, Size: metadata.Size}
		if metadata.Size <= 1024*1024 {
			data, err := os.ReadFile(filepath.Join(base, path))
			if err != nil {
				return nil, err
			}
			entry.Base64 = base64.StdEncoding.EncodeToString(data)
		}
		prepared = append(prepared, entry)
	}
	workspace, err := call.Workspace(base)
	if err != nil {
		return nil, err
	}
	payload["media_inputs"], _ = json.Marshal(prepared)
	payload["media_workspace"], _ = json.Marshal(filepath.ToSlash(workspace))
	return json.Marshal(payload)
}

func prepareCommandMedia(ctx context.Context, raw json.RawMessage, manifest AgentSkillManifest, call *tool.MediaCall, base string) (json.RawMessage, error) {
	properties, _ := manifest.Parameters["properties"].(map[string]any)
	var values map[string]json.RawMessage
	for name, property := range properties {
		spec, _ := property.(map[string]any)
		if spec["type"] != "media" {
			continue
		}
		if values == nil {
			if len(bytes.TrimSpace(raw)) == 0 {
				return raw, nil
			}
			if err := json.Unmarshal(raw, &values); err != nil {
				return nil, err
			}
		}
		value, ok := values[name]
		if !ok {
			continue
		}
		var id string
		if err := json.Unmarshal(value, &id); err != nil {
			return nil, fmt.Errorf("media argument %s must be a media ID", name)
		}
		path, _, err := call.Export(ctx, id, base)
		if err != nil {
			return nil, fmt.Errorf("media argument %s: %w", name, err)
		}
		values[name], _ = json.Marshal(filepath.ToSlash(path))
	}
	if values == nil {
		return raw, nil
	}
	return json.Marshal(values)
}

func resultFromStdoutWithMedia(ctx context.Context, out string, call *tool.MediaCall, base string) (*tool.Result, error) {
	trimmed := strings.TrimSpace(out)
	var structured struct {
		Content  string `json:"content"`
		Segments []struct {
			Type     llm.MessageSegmentType `json:"type"`
			Text     string                 `json:"text"`
			MediaID  string                 `json:"media_id"`
			Path     string                 `json:"path"`
			Name     string                 `json:"name"`
			MIMEType string                 `json:"mime_type"`
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(trimmed), &structured); err != nil || (structured.Content == "" && len(structured.Segments) == 0) {
		if trimmed == "" {
			return &tool.Result{}, nil
		}
		return &tool.Result{Content: truncateOutput(out)}, nil
	}
	result := &tool.Result{Content: truncateOutput(structured.Content)}
	if len(structured.Segments) == 0 {
		return result, nil
	}
	result.Segments = llm.TextSegments(result.Content)
	for _, segment := range structured.Segments {
		if segment.Type == llm.SegmentText {
			if segment.MediaID != "" || segment.Path != "" {
				return nil, fmt.Errorf("text segment cannot contain media")
			}
			result.Segments = append(result.Segments, llm.MessageSegment{Type: llm.SegmentText, Text: segment.Text})
			continue
		}
		if segment.Type != llm.SegmentImage && segment.Type != llm.SegmentFile {
			return nil, fmt.Errorf("unsupported skill segment type %q", segment.Type)
		}
		if (segment.MediaID == "") == (segment.Path == "") {
			return nil, fmt.Errorf("media segment requires exactly one of media_id or path")
		}
		if call == nil {
			return nil, fmt.Errorf("skill media runtime is not configured")
		}
		id := segment.MediaID
		if segment.Path != "" {
			metadata, err := call.Import(ctx, base, segment.Path, media.Input{Name: segment.Name, MIMEType: segment.MIMEType})
			if err != nil {
				return nil, err
			}
			id = metadata.ID
		}
		metadata, err := call.Retain(ctx, id)
		if err != nil {
			return nil, err
		}
		name, mimeType := segment.Name, segment.MIMEType
		if name == "" {
			name = metadata.Name
		}
		if mimeType == "" {
			mimeType = metadata.MIMEType
		}
		result.Segments = append(result.Segments, llm.MessageSegment{Type: segment.Type, Text: segment.Text, MediaID: id, Name: name, MIMEType: mimeType})
	}
	return result, nil
}

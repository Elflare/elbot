// 处理 Skill 内容中的媒体访问与转换。
package skill

import toolshare "elbot/internal/tool/share"

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
)

// prepareGoMedia changes only the stdin copy and only an explicit media_inputs field.
func prepareGoMedia(ctx context.Context, raw json.RawMessage, call toolshare.MediaCall, base string) (json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	value, ok := payload["media_inputs"]
	if !ok {
		return raw, nil
	}
	var inputs []toolshare.MediaInput
	if err := json.Unmarshal(value, &inputs); err != nil {
		return nil, fmt.Errorf("media_inputs: %w", err)
	}
	type preparedInput struct {
		MediaID  string `json:"media"`
		Path     string `json:"path"`
		Name     string `json:"name"`
		MIMEType string `json:"mime_type"`
		Size     int64  `json:"size"`
		Base64   string `json:"base64,omitempty"`
	}
	prepared := make([]preparedInput, 0, len(inputs))
	preparedByID := make(map[string]preparedInput, len(inputs))
	for _, input := range inputs {
		if entry, ok := preparedByID[input.MediaID]; ok {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			prepared = append(prepared, entry)
			continue
		}
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
		preparedByID[input.MediaID] = entry
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

func prepareCommandMedia(ctx context.Context, raw json.RawMessage, manifest AgentSkillManifest, call toolshare.MediaCall, base string) (json.RawMessage, error) {
	properties, _ := manifest.Parameters["properties"].(map[string]any)
	if !schemaContainsMedia(manifest.Parameters) || len(bytes.TrimSpace(raw)) == 0 {
		return raw, nil
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	for name, property := range properties {
		value, ok := values[name]
		if !ok || !schemaContainsMedia(property) {
			continue
		}
		prepared, err := prepareMediaValue(ctx, value, property, call, base, name)
		if err != nil {
			return nil, err
		}
		values[name] = prepared
	}
	return json.Marshal(values)
}

func schemaContainsMedia(value any) bool {
	schema, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch schema["type"] {
	case "media":
		return true
	case "array":
		return schemaContainsMedia(schema["items"])
	case "object":
		properties, _ := schema["properties"].(map[string]any)
		for _, property := range properties {
			if schemaContainsMedia(property) {
				return true
			}
		}
	}
	return false
}

func prepareMediaValue(ctx context.Context, raw json.RawMessage, schemaValue any, call toolshare.MediaCall, base, name string) (json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("media argument %s must not be null", name)
	}
	schema, _ := schemaValue.(map[string]any)
	switch schema["type"] {
	case "media":
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			return nil, fmt.Errorf("media argument %s must be a media ID", name)
		}
		path, _, err := call.Export(ctx, id, base)
		if err != nil {
			return nil, fmt.Errorf("media argument %s: %w", name, err)
		}
		return json.Marshal(filepath.ToSlash(path))
	case "array":
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("media argument %s must be an array", name)
		}
		for i, item := range items {
			prepared, err := prepareMediaValue(ctx, item, schema["items"], call, base, fmt.Sprintf("%s[%d]", name, i))
			if err != nil {
				return nil, err
			}
			items[i] = prepared
		}
		return json.Marshal(items)
	case "object":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("media argument %s must be an object", name)
		}
		properties, _ := schema["properties"].(map[string]any)
		for field, property := range properties {
			value, ok := fields[field]
			if !ok || !schemaContainsMedia(property) {
				continue
			}
			prepared, err := prepareMediaValue(ctx, value, property, call, base, name+"."+field)
			if err != nil {
				return nil, err
			}
			fields[field] = prepared
		}
		return json.Marshal(fields)
	default:
		return raw, nil
	}
}

func resultFromStdoutWithMedia(ctx context.Context, out string, call toolshare.MediaCall, base string) (*toolshare.Result, error) {
	trimmed := strings.TrimSpace(out)
	var structured struct {
		Content  string `json:"content"`
		Segments []struct {
			Type     llm.MessageSegmentType `json:"type"`
			Text     string                 `json:"text"`
			MediaID  string                 `json:"media"`
			Path     string                 `json:"path"`
			Name     string                 `json:"name"`
			MIMEType string                 `json:"mime_type"`
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(trimmed), &structured); err != nil || (structured.Content == "" && len(structured.Segments) == 0) {
		if trimmed == "" {
			return &toolshare.Result{}, nil
		}
		return &toolshare.Result{Content: truncateOutput(out)}, nil
	}
	result := &toolshare.Result{Content: truncateOutput(structured.Content)}
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
			return nil, fmt.Errorf("media segment requires exactly one of media or path")
		}
		if call == nil {
			return nil, fmt.Errorf("skill media runtime is not configured")
		}
		id := segment.MediaID
		if segment.Path != "" {
			metadata, err := call.Import(ctx, base, segment.Path, toolshare.MediaImportOptions{Name: segment.Name, MIMEType: segment.MIMEType})
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

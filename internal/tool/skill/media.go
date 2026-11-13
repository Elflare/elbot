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
	"elbot/internal/operationresult"
)

// prepareGoMedia changes only the stdin copy and only an explicit media_inputs field.
func prepareGoMedia(ctx context.Context, raw json.RawMessage, call toolshare.MediaCall, base string) (operationresult.Result[json.RawMessage], error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return operationresult.Result[json.RawMessage]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	value, ok := payload["media_inputs"]
	if !ok {
		return operationresult.Result[json.RawMessage]{Status: operationresult.Success, Data: raw}, nil
	}
	var inputs []toolshare.MediaInput
	if err := json.Unmarshal(value, &inputs); err != nil {
		return operationresult.Result[json.RawMessage]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("media_inputs: %v", err)}, nil
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
				return operationresult.Result[json.RawMessage]{}, err
			}
			prepared = append(prepared, entry)
			continue
		}
		path, metadata, err := call.Export(ctx, input.MediaID, base)
		if err != nil {
			return operationresult.Result[json.RawMessage]{}, err
		}
		if metadata.Status != operationresult.Success {
			return operationresult.Result[json.RawMessage]{Status: metadata.Status, Message: metadata.Message}, nil
		}
		entry := preparedInput{MediaID: input.MediaID, Path: filepath.ToSlash(path), Name: metadata.Data.Name, MIMEType: metadata.Data.MIMEType, Size: metadata.Data.Size}
		if metadata.Data.Size <= 1024*1024 {
			data, err := os.ReadFile(filepath.Join(base, path))
			if err != nil {
				return operationresult.Result[json.RawMessage]{}, err
			}
			entry.Base64 = base64.StdEncoding.EncodeToString(data)
		}
		preparedByID[input.MediaID] = entry
		prepared = append(prepared, entry)
	}
	workspace, err := call.Workspace(base)
	if err != nil {
		return operationresult.Result[json.RawMessage]{}, err
	}
	payload["media_inputs"], _ = json.Marshal(prepared)
	payload["media_workspace"], _ = json.Marshal(filepath.ToSlash(workspace))
	data, err := json.Marshal(payload)
	if err != nil {
		return operationresult.Result[json.RawMessage]{}, err
	}
	return operationresult.Result[json.RawMessage]{Status: operationresult.Success, Data: data}, nil
}

func prepareCommandMedia(ctx context.Context, raw json.RawMessage, manifest AgentSkillManifest, call toolshare.MediaCall, base string) (operationresult.Result[json.RawMessage], error) {
	properties, _ := manifest.Parameters["properties"].(map[string]any)
	if !schemaContainsMedia(manifest.Parameters) || len(bytes.TrimSpace(raw)) == 0 {
		return operationresult.Result[json.RawMessage]{Status: operationresult.Success, Data: raw}, nil
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return operationresult.Result[json.RawMessage]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	for name, property := range properties {
		value, ok := values[name]
		if !ok || !schemaContainsMedia(property) {
			continue
		}
		prepared, err := prepareMediaValue(ctx, value, property, call, base, name)
		if err != nil {
			return operationresult.Result[json.RawMessage]{}, err
		}
		if prepared.Status != operationresult.Success {
			return prepared, nil
		}
		values[name] = prepared.Data
	}
	data, err := json.Marshal(values)
	if err != nil {
		return operationresult.Result[json.RawMessage]{}, err
	}
	return operationresult.Result[json.RawMessage]{Status: operationresult.Success, Data: data}, nil
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

func prepareMediaValue(ctx context.Context, raw json.RawMessage, schemaValue any, call toolshare.MediaCall, base, name string) (operationresult.Result[json.RawMessage], error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return operationresult.Result[json.RawMessage]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("media argument %s must not be null", name)}, nil
	}
	schema, _ := schemaValue.(map[string]any)
	switch schema["type"] {
	case "media":
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			return operationresult.Result[json.RawMessage]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("media argument %s must be a media ID", name)}, nil
		}
		path, metadata, err := call.Export(ctx, id, base)
		if err != nil {
			return operationresult.Result[json.RawMessage]{}, fmt.Errorf("media argument %s: %w", name, err)
		}
		if metadata.Status != operationresult.Success {
			return operationresult.Result[json.RawMessage]{Status: metadata.Status, Message: metadata.Message}, nil
		}
		data, err := json.Marshal(filepath.ToSlash(path))
		if err != nil {
			return operationresult.Result[json.RawMessage]{}, err
		}
		return operationresult.Result[json.RawMessage]{Status: operationresult.Success, Data: data}, nil
	case "array":
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return operationresult.Result[json.RawMessage]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("media argument %s must be an array", name)}, nil
		}
		for i, item := range items {
			prepared, err := prepareMediaValue(ctx, item, schema["items"], call, base, fmt.Sprintf("%s[%d]", name, i))
			if err != nil {
				return operationresult.Result[json.RawMessage]{}, err
			}
			if prepared.Status != operationresult.Success {
				return prepared, nil
			}
			items[i] = prepared.Data
		}
		data, err := json.Marshal(items)
		if err != nil {
			return operationresult.Result[json.RawMessage]{}, err
		}
		return operationresult.Result[json.RawMessage]{Status: operationresult.Success, Data: data}, nil
	case "object":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return operationresult.Result[json.RawMessage]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("media argument %s must be an object", name)}, nil
		}
		properties, _ := schema["properties"].(map[string]any)
		for field, property := range properties {
			value, ok := fields[field]
			if !ok || !schemaContainsMedia(property) {
				continue
			}
			prepared, err := prepareMediaValue(ctx, value, property, call, base, name+"."+field)
			if err != nil {
				return operationresult.Result[json.RawMessage]{}, err
			}
			if prepared.Status != operationresult.Success {
				return prepared, nil
			}
			fields[field] = prepared.Data
		}
		data, err := json.Marshal(fields)
		if err != nil {
			return operationresult.Result[json.RawMessage]{}, err
		}
		return operationresult.Result[json.RawMessage]{Status: operationresult.Success, Data: data}, nil
	default:
		return operationresult.Result[json.RawMessage]{Status: operationresult.Success, Data: raw}, nil
	}
}

func resultFromStdoutWithMedia(ctx context.Context, out string, call toolshare.MediaCall, base string) (operationresult.Result[*toolshare.Result], error) {
	trimmed := strings.TrimSpace(out)
	var structured map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &structured); err != nil || (structured["content"] == nil && structured["segments"] == nil) {
		if trimmed == "" {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{}}, nil
		}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: truncateOutput(out)}}, nil
	}
	result := &toolshare.Result{}
	if raw, ok := structured["content"]; ok {
		if err := json.Unmarshal(raw, &result.Content); err != nil {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: err.Error()}, nil
		}
	}
	var segments []struct {
		Type     llm.MessageSegmentType `json:"type"`
		Text     string                 `json:"text"`
		MediaID  string                 `json:"media"`
		Path     string                 `json:"path"`
		Name     string                 `json:"name"`
		MIMEType string                 `json:"mime_type"`
	}
	if raw, ok := structured["segments"]; ok {
		if err := json.Unmarshal(raw, &segments); err != nil {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: err.Error()}, nil
		}
	}
	result.Content = truncateOutput(result.Content)
	if len(segments) == 0 {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, nil
	}
	result.Segments = llm.TextSegments(result.Content)
	for _, segment := range segments {
		if segment.Type == llm.SegmentText {
			if segment.MediaID != "" || segment.Path != "" {
				return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: "text segment cannot contain media"}, nil
			}
			result.Segments = append(result.Segments, llm.MessageSegment{Type: llm.SegmentText, Text: segment.Text})
			continue
		}
		if segment.Type != llm.SegmentImage && segment.Type != llm.SegmentFile {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: fmt.Sprintf("unsupported skill segment type %q", segment.Type)}, nil
		}
		if (segment.MediaID == "") == (segment.Path == "") {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: "media segment requires exactly one of media or path"}, nil
		}
		if call == nil {
			return operationresult.Result[*toolshare.Result]{}, fmt.Errorf("skill media runtime is not configured")
		}
		id := segment.MediaID
		if segment.Path != "" {
			metadata, err := call.Import(ctx, base, segment.Path, toolshare.MediaImportOptions{Name: segment.Name, MIMEType: segment.MIMEType})
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err
			}
			if metadata.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: metadata.Status, Message: metadata.Message}, nil
			}
			id = metadata.Data.ID
		}
		metadata, err := call.Retain(ctx, id)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err
		}
		if metadata.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: metadata.Status, Message: metadata.Message}, nil
		}
		name, mimeType := segment.Name, segment.MIMEType
		if name == "" {
			name = metadata.Data.Name
		}
		if mimeType == "" {
			mimeType = metadata.Data.MIMEType
		}
		result.Segments = append(result.Segments, llm.MessageSegment{Type: segment.Type, Text: segment.Text, MediaID: id, Name: name, MIMEType: mimeType})
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, nil
}

package responses

import (
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

func inputContent(segments []llm.MessageSegment) []api.InputContent {
	var content []api.InputContent
	imageIndex := 0
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			content = append(content, api.InputContent{Type: "input_text", Text: segment.Text})
		case llm.SegmentImage:
			imageIndex++
			if label := llm.ImageReferenceText(segment, imageIndex); label != "" {
				content = append(content, api.InputContent{Type: "input_text", Text: label})
			}
			content = append(content, api.InputContent{Type: "input_image", ImageURL: segment.URL})
		case llm.SegmentFile:
			part := api.InputContent{Type: "input_file", Filename: segment.Name}
			if strings.HasPrefix(segment.URL, "data:") {
				part.FileData = segment.URL
				if part.Filename == "" {
					part.Filename = "file"
				}
			} else {
				part.FileURL = segment.URL
			}
			content = append(content, part)
		}
	}
	return content
}

func nativeItem(callID string, segments []llm.MessageSegment) (api.Item, error) {
	content := inputContent(segments)
	if callID == "" {
		return api.InputMessage("user", content)
	}
	var output json.RawMessage
	var err error
	textOnly := true
	for _, segment := range segments {
		if segment.Type != llm.SegmentText {
			textOnly = false
			break
		}
	}
	if textOnly {
		output, err = json.Marshal(llm.SegmentsContentText(segments))
	} else {
		output, err = json.Marshal(content)
	}
	if err != nil {
		return api.Item{}, err
	}
	return api.FunctionCallOutput(callID, output)
}

func queuedInput(sessionID, messageID, exchangeID, callID string, segments []llm.MessageSegment) (storage.NativeInput, error) {
	item, err := nativeItem(callID, segments)
	if err != nil {
		return storage.NativeInput{}, err
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return storage.NativeInput{}, err
	}
	canonical := append([]llm.MessageSegment(nil), segments...)
	for i := range canonical {
		if canonical[i].MediaID != "" {
			canonical[i].URL = ""
		}
	}
	media, err := json.Marshal(canonical)
	if err != nil {
		return storage.NativeInput{}, err
	}
	return storage.NativeInput{ID: storage.NewID(), SessionID: sessionID, MessageID: messageID, ExchangeID: exchangeID, CallID: callID, ItemJSON: string(raw), MediaJSON: string(media)}, nil
}

func inputSegments(input storage.NativeInput) ([]llm.MessageSegment, error) {
	var segments []llm.MessageSegment
	if err := json.Unmarshal([]byte(input.MediaJSON), &segments); err != nil {
		return nil, fmt.Errorf("decode native input material: %w", err)
	}
	return segments, nil
}

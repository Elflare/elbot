package agent

import (
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/platform"
)

func platformSegmentsToLLM(segments []platform.MessageSegment, fallbackText string) []llm.MessageSegment {
	out := make([]llm.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case platform.SegmentText:
			if segment.Text != "" {
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: segment.Text})
			}
		case platform.SegmentImage:
			if segment.URL != "" || segment.MediaID != "" {
				out = append(out, llm.MessageSegment{Type: llm.SegmentImage, MediaID: segment.MediaID, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name})
			} else {
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: fileSegmentText(segment.Name, "图片")})
			}
		case platform.SegmentFile:
			// TODO: 后续支持语音、视频和普通文件的真实模型输入；当前统一回滚为文本描述。
			out = append(out, llm.MessageSegment{Type: llm.SegmentFile, MediaID: segment.MediaID, URL: segment.URL, Text: fileSegmentText(segment.Name, segment.Text), MIMEType: segment.MIMEType, Name: segment.Name})
		}
	}
	if len(out) == 0 {
		return llm.TextSegments(fallbackText)
	}
	return out
}

func fileSegmentText(name, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		fallback = "文件"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "[" + fallback + "]"
	}
	return fmt.Sprintf("[%s: %s]", fallback, name)
}

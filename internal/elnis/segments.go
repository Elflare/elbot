package elnis

import (
	"fmt"
	"net/url"
	"strings"

	"elbot/internal/llm"
)

func validateSegmentURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("url is required for image/file segments")
	}
	if strings.HasPrefix(rawURL, "data:") {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("url host is empty")
	}
	return nil
}

func segmentsContentText(segments []Segment) string {
	var b strings.Builder
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentKindText:
			b.WriteString(seg.Text)
		case SegmentKindImage:
			label := firstNonEmptyStr(seg.Name, seg.URL)
			if label == "" {
				label = "[图片]"
			}
			b.WriteString(fmt.Sprintf("[图片: %s]", label))
		case SegmentKindFile:
			label := firstNonEmptyStr(seg.Name, seg.URL)
			if label == "" {
				label = "[文件]"
			}
			b.WriteString(fmt.Sprintf("[文件: %s]", label))
		}
	}
	return strings.TrimSpace(b.String())
}

func segmentsLLM(segments []Segment) []llm.MessageSegment {
	var out []llm.MessageSegment
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentKindText:
			out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: seg.Text})
		case SegmentKindImage:
			out = append(out, llm.MessageSegment{Type: llm.SegmentImage, URL: seg.URL, Name: seg.Name, MIMEType: seg.MIMEType})
		case SegmentKindFile:
			out = append(out, llm.MessageSegment{Type: llm.SegmentFile, URL: seg.URL, Name: seg.Name, MIMEType: seg.MIMEType})
		}
	}
	return out
}

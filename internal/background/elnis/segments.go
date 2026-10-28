package elnis

import (
	"fmt"
	"strings"
)

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

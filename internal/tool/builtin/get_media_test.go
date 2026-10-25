package builtin

import (
	"strings"
	"testing"
)

func TestGetMediaSchemaAndHistoryIDTruncation(t *testing.T) {
	schema := (GetMediaTool{}).Schema()
	properties := schema.Parameters["properties"].(map[string]any)
	outer := properties["media_index"].(map[string]any)
	inner := outer["items"].(map[string]any)
	item := inner["items"].(map[string]any)
	if outer["type"] != "array" || inner["type"] != "array" || item["type"] != "integer" {
		t.Fatalf("media index schema = %#v", outer)
	}
	id := "media:" + strings.Repeat("a", 64)
	label := "  1. [图片 " + id + "]"
	result := chatHistoryResultLines([]string{strings.Repeat("文", chatHistoryTextLimit-20), label}, "...[查询结果过长，已截断]")
	if strings.Contains(result, "media:") || !strings.Contains(result, "已截断") {
		t.Fatalf("partial ID escaped truncation: %s", result)
	}
}

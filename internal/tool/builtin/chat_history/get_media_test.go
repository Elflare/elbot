package chat_history

import (
	"strings"
	"testing"
)

func TestGetMediaSchemaAndIndependentPreviewLimits(t *testing.T) {
	schema := (GetMediaTool{}).Definition().Schema()
	properties := schema.Parameters["properties"].(map[string]any)
	outer := properties["media_index"].(map[string]any)
	inner := outer["items"].(map[string]any)
	item := inner["items"].(map[string]any)
	if outer["type"] != "array" || inner["type"] != "array" || item["type"] != "integer" {
		t.Fatalf("media index schema = %#v", outer)
	}
	body, quote := strings.Repeat("正", 301), strings.Repeat("引", 301)
	row := Message{Seq: 1, Text: body, Quote: "[引用 h2：助手：" + quote + "]"}
	out := strings.Join(formatHistoryRows([]Message{row}, 0, false, false), "\n")
	if strings.Count(out, "正") != 300 || !strings.Contains(out, "[引用 h2：助手：") || strings.Contains(out, quote) || strings.Count(out, "单条消息过长") != 2 {
		t.Fatal(out)
	}
	full := strings.Join(formatHistoryRows([]Message{row}, 1, true, false), "\n")
	if !strings.Contains(full, body) || !strings.Contains(full, quote) {
		t.Fatal(full)
	}
}

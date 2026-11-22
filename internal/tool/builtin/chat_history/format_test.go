package chat_history

import (
	"strings"
	"testing"
)

func TestAssistantLabelOnlyInParticipantList(t *testing.T) {
	rows := []Message{
		{Seq: 1, SenderID: "user", SenderName: "芙蕾娅", Text: "问"},
		{Seq: 2, SenderID: "bot", SenderName: "ATRI", IsAssistant: true, Text: "答一"},
		{Seq: 3, SenderID: "bot", IsAssistant: true, Text: "答二"},
	}
	got := strings.Join(formatHistoryRows(rows, 0, false, false), "\n")
	want := `0001-01-01
h1 00:00:00 u1: 问
h2 00:00:00 u2: 答一
h3 00:00:00 u2: 答二
参与者
u1 芙蕾娅 user
u2 ATRI（助手） bot`
	if got != want {
		t.Fatalf("history:\n%s\nwant:\n%s", got, want)
	}
}

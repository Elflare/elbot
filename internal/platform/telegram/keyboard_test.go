package telegram

import (
	"elbot/internal/delivery"
	"strings"
	"testing"
)

func TestOverlongCallbackIsManual(t *testing.T) {
	valid := strings.Repeat("x", 64)
	long := "-sessions 2 -k " + strings.Repeat("查", 30)
	text, buttons := keyboardActions("列表", []delivery.Action{{Label: "短", Command: valid}, {Label: "下一页", Command: long}})
	if len(buttons) != 1 || buttons[0].Command != valid || strings.Count(text, long) != 1 || !strings.Contains(text, "手动输入") || strings.Contains(text, valid) {
		t.Fatal(text, buttons)
	}
}

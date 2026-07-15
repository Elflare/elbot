package contextmgr

import (
	"strings"
	"testing"

	"elbot/internal/storage"
)

func TestCompactPromptAndSummaryAssembly(t *testing.T) {
	prompt := compactPrompt([]CompactMessage{
		{Role: storage.RoleUser, Content: "B"},
		{Role: storage.RoleAssistant, Content: "C", ToolCalls: []CompactToolCall{{Name: "shell", Arguments: `{"command":"go test ./..."}`}}},
		{Role: storage.RoleAssistant, Content: "H"},
	}, []string{"B", "G"})
	for _, want := range []string{"上下文内容：", "user: B", "assistant: C", `tool_call: name=shell arguments={"command":"go test ./..."}`, "用户原话：\n1. B\n2. G"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "tool result") {
		t.Fatalf("prompt contains tool result: %s", prompt)
	}

	got := assembleSummary("K\n", []string{"B", "G"})
	want := "K\n\n以下是用户原话：\n1. B\n2. G"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got := assembleSummary("K", nil); got != "K" {
		t.Fatalf("summary without inputs = %q", got)
	}
}

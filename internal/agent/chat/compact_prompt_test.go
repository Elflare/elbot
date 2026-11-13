package chat

import (
	"strings"
	"testing"

	"elbot/internal/contextmgr"
	"elbot/internal/storage"
)

func TestCompactPromptUsesConversationOnly(t *testing.T) {
	prompt := compactPrompt([]CompactMessage{
		{Role: storage.RoleUser, Content: "B"},
		{Role: storage.RoleAssistant, Content: "C", ToolCalls: []CompactToolCall{{Name: "shell", Arguments: `{"command":"go test ./..."}`}}},
		{Role: storage.RoleAssistant, Content: "H"},
	})
	for _, want := range []string{"上下文内容：", "user: B", "assistant: C", `tool_call: name=shell arguments={"command":"go test ./..."}`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "用户原话：") {
		t.Fatalf("prompt duplicated user inputs:\n%s", prompt)
	}
}

func TestCompactUserInputsSkipsSyntheticFirstMessageAfterCompaction(t *testing.T) {
	messages := []storage.Message{
		{Role: storage.RoleUser, Content: "summary + prior inputs"},
		{Role: storage.RoleAssistant, Content: "answer"},
		{Role: storage.RoleUser, Content: "U"},
		{Role: storage.RoleUser, Content: "U2"},
	}
	got := compactUserInputs(messages, &contextmgr.CompactState{Inputs: []string{"B"}}, true)
	want := []string{"B", "U", "U2"}
	if len(got) != len(want) {
		t.Fatalf("user inputs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("user inputs = %#v, want %#v", got, want)
		}
	}

	first := compactUserInputs(messages[2:], nil, false)
	if len(first) != 2 || first[0] != "U" || first[1] != "U2" {
		t.Fatalf("first-compaction inputs = %#v", first)
	}
}

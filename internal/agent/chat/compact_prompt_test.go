package chat

import (
	"strings"
	"testing"

	"elbot/internal/contextmgr"
	"elbot/internal/storage"
)

func TestCompactPrompt(t *testing.T) {
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
}

func TestCompactUserInputsPreservesPriorAndCurrentOriginalInputs(t *testing.T) {
	message := storage.Message{Role: storage.RoleUser, Content: "context summary\n\ncurrent input", Metadata: `{"elyph_rules_shown":true}`}
	if err := setCompactUserInput(&message, "current input"); err != nil {
		t.Fatal(err)
	}
	got, err := compactUserInputs([]storage.Message{
		message,
		{ID: "next", Role: storage.RoleUser, Content: "later input"},
	}, &contextmgr.CompactState{UserInputs: []string{"prior input"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"prior input", "current input", "later input"}
	if len(got) != len(want) {
		t.Fatalf("user inputs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("user inputs = %#v, want %#v", got, want)
		}
	}
	fields, err := storage.DecodeSessionMetadata(message.Metadata)
	if err != nil || string(fields["elyph_rules_shown"]) != "true" {
		t.Fatalf("existing message metadata was not preserved: %s (%v)", message.Metadata, err)
	}
}

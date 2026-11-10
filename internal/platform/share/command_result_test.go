package share

import (
	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/llm"
	"elbot/internal/tool"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatCommandResultPreservesRawAcrossTransport(t *testing.T) {
	raw := control.ListSessionsResult{Items: []control.SessionSummaryData{{SessionData: control.SessionData{ID: "s"}, Messages: []control.MessageData{{Content: strings.Repeat("正文", 40), Segments: []llm.MessageSegment{{Type: llm.SegmentFile, Name: "note.txt", MediaID: "private-reference"}}}}}}}
	result := &command.Result{Kind: command.ResultKindSessions, Data: raw}
	bytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var remote command.Result
	if err := json.Unmarshal(bytes, &remote); err != nil {
		t.Fatal(err)
	}
	req := command.Request{Name: "sessions", Prefix: "!"}
	text := FormatCommandResult(req, result)
	if got := FormatCommandResult(req, &remote); got != text {
		t.Fatalf("local=%q remote=%q", text, got)
	}
	if !strings.Contains(text, "note.txt") || !strings.Contains(string(bytes), "private-reference") || !strings.Contains(string(bytes), strings.Repeat("正文", 40)) {
		t.Fatalf("result was trimmed: %s", bytes)
	}
	after, _ := json.Marshal(result)
	if string(after) != string(bytes) {
		t.Fatal("formatting mutated raw")
	}
}
func TestFormatCommandResultUsesCallingOperation(t *testing.T) {
	for _, tc := range []struct {
		req    command.Request
		result command.Result
		want   string
	}{
		{command.Request{Name: "new"}, command.Result{Kind: command.ResultKindSession, Data: control.NewSessionResult{}}, "New Session"},
		{command.Request{Name: "hooks", Args: "reload"}, command.Result{Kind: command.ResultKindHooks, Data: control.ReloadHooksResult{Notices: []string{"warning"}}}, "warning"},
		{command.Request{Name: "tools", Args: "reload"}, command.Result{Kind: command.ResultKindTools}, "skills reloaded"},
	} {
		if got := FormatCommandResult(tc.req, &tc.result); !strings.Contains(got, tc.want) {
			t.Fatalf("got=%q want=%q", got, tc.want)
		}
	}
}

func TestFormatDetailCommandCombinesStructuredBlocks(t *testing.T) {
	result := &command.Result{Kind: command.ResultKindText, Data: control.CurrentConfirmationResult{
		ToolName: "edit_file",
		Risk:     "high",
		Detail: []tool.ConfirmationDetailBlock{
			{Kind: tool.ConfirmationDetailText, Content: "文件：notes.md"},
			{Kind: tool.ConfirmationDetailDiff, Content: "-before\n+after"},
			{Kind: tool.ConfirmationDetailArguments, Content: map[string]any{"path": "notes.md"}},
		},
	}}
	got := FormatCommandResult(command.Request{Name: "detail"}, result)
	for _, want := range []string{"edit_file [high]", "文件：notes.md", "-before\n+after", `"path": "notes.md"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail output %q does not contain %q", got, want)
		}
	}
}

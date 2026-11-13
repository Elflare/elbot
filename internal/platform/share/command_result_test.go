package share

import (
	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFormatCommandResultPreservesRawAcrossTransport(t *testing.T) {
	raw := control.ListSessionsResult{Items: []control.SessionSummaryData{{SessionData: control.SessionData{ID: "s"}, Messages: []control.MessageData{{Content: strings.Repeat("正文", 40), Segments: []llm.MessageSegment{{Type: llm.SegmentFile, Name: "note.txt", MediaID: "private-reference"}}}}}}}
	result := &command.Result{Status: operationresult.Success, Kind: command.ResultKindSessions, Data: raw}
	bytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var remote command.Result
	if err := json.Unmarshal(bytes, &remote); err != nil {
		t.Fatal(err)
	}
	req := command.Request{Name: "sessions", Prefix: "!"}
	text, err := FormatCommandResult(req, *result)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := FormatCommandResult(req, remote); err != nil || got != text {
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

func TestCommandResultOutputsUsesBusinessMessageAndGoError(t *testing.T) {
	request := command.Request{Name: "messages"}
	result := command.Result{Status: operationresult.NoCurrentSession, Message: "当前没有会话", Kind: command.ResultKindMessages, Data: struct{}{}}
	outputs, err := CommandResultOutputs(request, result, nil)
	if err != nil || len(outputs) != 1 || outputs[0].Text != result.Message {
		t.Fatalf("business outputs=%+v err=%v", outputs, err)
	}

	failure := errors.New("database unavailable")
	outputs, err = CommandResultOutputs(request, command.Result{}, failure)
	if err != nil || len(outputs) != 1 || outputs[0].Text != failure.Error() {
		t.Fatalf("error outputs=%+v err=%v", outputs, err)
	}

	outputs, err = CommandResultOutputs(request, command.Result{Status: operationresult.Success, Kind: command.ResultKindMessages, Data: control.SessionMessagesResult{}}, nil)
	if err != nil || len(outputs) != 0 {
		t.Fatalf("empty success outputs=%+v err=%v", outputs, err)
	}
}

func TestFormatCommandResultUsesCallingOperation(t *testing.T) {
	for _, tc := range []struct {
		req    command.Request
		result command.Result
		want   string
	}{
		{command.Request{Name: "new"}, command.Result{Status: operationresult.Success, Kind: command.ResultKindSession, Data: control.NewSessionResult{}}, "New Session"},
		{command.Request{Name: "hooks", Args: "reload"}, command.Result{Status: operationresult.Success, Kind: command.ResultKindHooks, Data: control.ReloadHooksResult{Notices: []string{"warning"}}}, "warning"},
		{command.Request{Name: "tools", Args: "reload"}, command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "skills reloaded"}}, "skills reloaded"},
	} {
		got, err := FormatCommandResult(tc.req, tc.result)
		if err != nil || !strings.Contains(got, tc.want) {
			t.Fatalf("got=%q want=%q", got, tc.want)
		}
	}
}

func TestSessionPaginationPreservesAllPlatformsFlag(t *testing.T) {
	result := command.Result{Status: operationresult.Success, Kind: command.ResultKindSessions, Data: control.ListSessionsResult{
		Page: control.Page{Page: 1, PageSize: 1, HasNext: true, TotalPages: 2},
	}}
	got, err := FormatCommandResult(command.Request{Name: "sessions", Prefix: "/", Args: "-a"}, result)
	if err != nil || !strings.Contains(got, "next: /sessions 2 --all") {
		t.Fatalf("pagination dropped --all: %q", got)
	}
	resume, err := FormatCommandResult(command.Request{Name: "resume", Prefix: "/", Args: "--all"}, result)
	if err != nil || !strings.Contains(resume, "next: /resume --page 2 --all") {
		t.Fatalf("resume pagination dropped --all: %q", resume)
	}
}

func TestFormatDetailCommandCombinesStructuredBlocks(t *testing.T) {
	result := command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: control.CurrentConfirmationResult{
		ToolName: "edit_file",
		Risk:     "high",
		Detail: []tool.ConfirmationDetailBlock{
			{Kind: tool.ConfirmationDetailText, Content: "文件：notes.md"},
			{Kind: tool.ConfirmationDetailDiff, Content: "-before\n+after"},
			{Kind: tool.ConfirmationDetailArguments, Content: map[string]any{"path": "notes.md"}},
		},
	}}
	got, err := FormatCommandResult(command.Request{Name: "detail"}, result)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"edit_file [high]", "文件：notes.md", "-before\n+after", `"path": "notes.md"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail output %q does not contain %q", got, want)
		}
	}
}

package share

import (
	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/delivery"
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
	result := &command.Result{Status: operationresult.Success, Kind: command.ResultKindSessions, Blocks: []delivery.ContentBlock{{Kind: delivery.ContentSessionList, Content: raw}}}
	bytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var remote command.Result
	if err := json.Unmarshal(bytes, &remote); err != nil {
		t.Fatal(err)
	}
	text := FormatCommandResult(*result)
	if got := FormatCommandResult(remote); got != text {
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
	result := command.TextResult(operationresult.NoCurrentSession, "当前没有会话")
	outputs := CommandResultOutputs(result, nil)
	if len(outputs) != 1 || delivery.FallbackText(outputs[0]) != "当前没有会话" {
		t.Fatal(outputs)
	}
	failure := errors.New("database unavailable")
	outputs = CommandResultOutputs(command.Result{}, failure)
	if len(outputs) != 1 || outputs[0].Text != failure.Error() {
		t.Fatal(outputs)
	}
	if outputs = CommandResultOutputs(command.Result{Status: operationresult.Success}, nil); len(outputs) != 0 {
		t.Fatal(outputs)
	}
}
func TestFormatDetailCommandCombinesStructuredBlocks(t *testing.T) {
	result := command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Blocks: []tool.ConfirmationDetailBlock{
		{Kind: tool.ConfirmationDetailText, Content: "edit_file [high]: confirm"},
		{Kind: tool.ConfirmationDetailFields, Content: []tool.ConfirmationField{{Key: "path", Label: "文件", Value: "notes.md"}}},
		{Kind: tool.ConfirmationDetailDiff, Content: "-before\n+after"},
	}}
	got := FormatCommandResult(result)
	for _, want := range []string{"edit_file [high]", "文件：notes.md", "-before\n+after"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail output %q does not contain %q", got, want)
		}
	}
}

func TestRiskPreviewDoesNotTrimWireOrDetail(t *testing.T) {
	diff := "--- a/example.go\n+++ b/example.go\n@@ -1,2 +1,82 @@\n context\n---old\n+++new\n" + strings.Repeat("+变更\n", 80) + "+last"
	detail := []delivery.ContentBlock{
		{Kind: delivery.ContentFields, Content: []delivery.ContentField{{Key: "path", Label: "文件", Value: "example.go"}, {Key: "created", Label: "创建文件", Value: false}}},
		{Kind: delivery.ContentDiff, Content: diff},
	}
	out := delivery.Output{Kind: delivery.KindText, ShowActionHints: true, Blocks: []delivery.ContentBlock{{Kind: delivery.ContentText, Content: "edit [high]"}, {Kind: delivery.ContentPreview, Content: detail}, {Kind: delivery.ContentText, Content: "risk reason"}}, Actions: []delivery.Action{{Command: "-c", Label: "确认"}}}
	for _, buttons := range []bool{false, true} {
		s := delivery.OutputBody(out, buttons)
		if strings.Contains(s, "+last") || !strings.Contains(s, "example.go\n---old\n+++new\n+变更…") || strings.Contains(s, "创建文件") || !strings.Contains(s, "risk reason") || !strings.Contains(s, "-c 确认") {
			t.Fatal(s)
		}
	}
	wire, err := json.Marshal(out.Blocks)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []delivery.ContentBlock
	if err = json.Unmarshal(wire, &blocks); err != nil {
		t.Fatal(err)
	}
	if blocks[1].Content.([]delivery.ContentBlock)[1].Content != diff {
		t.Fatal("wire diff trimmed")
	}
	if !strings.Contains(FormatCommandResult(command.Result{Blocks: detail}), diff) {
		t.Fatal("detail trimmed")
	}
}

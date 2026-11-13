package command

import (
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandResultWireKeepsDataAndExcludesExecutionInternals(t *testing.T) {
	zero := int64(0)
	result := FromOperationResult(operationresult.Result[control.StatusResult]{Status: operationresult.Success, Data: control.StatusResult{Context: control.ContextData{Usage: &llm.Usage{TotalTokens: &zero}}}}, ResultKindStatus)
	result.Confirmation = &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认", Command: "/delete s --confirm"}}}
	result.Continuation = &control.Continuation{Kind: "input", Text: "private continuation", SessionID: "s"}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, part := range []string{`"kind":"status"`, `"TotalTokens":0`, `"PromptTokens":null`, `"confirmation":{"actions":[{"id":"confirm","label":"确认","command":"/delete s --confirm"}]}`} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %s: %s", part, text)
		}
	}
	if strings.Contains(text, "private continuation") {
		t.Fatal("continuation leaked")
	}
	failure := FromOperationResult(operationresult.Result[control.StatusResult]{Status: operationresult.Forbidden, Message: "not allowed", Data: control.StatusResult{}}, ResultKindStatus)
	raw, err = json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Result
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != operationresult.Forbidden || decoded.Message != failure.Message || decoded.Data != nil {
		t.Fatalf("failure result=%+v", decoded)
	}
}

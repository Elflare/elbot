package command

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/fileops"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCommandResultWireKeepsDataAndExcludesExecutionInternals(t *testing.T) {
	zero := int64(0)
	result := Result{Kind: ResultKindStatus, Data: StatusData{Context: ContextData{Usage: &TokenUsage{TotalTokens: &zero}}}, Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认", Command: "/delete s --confirm"}}}, Continuation: &Continuation{Kind: ContinuationInput, Text: "private continuation", SessionID: "s"}}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, part := range []string{`"kind":"status"`, `"total_tokens":0`, `"prompt_tokens":null`, `"confirmation":{"actions":[{"id":"confirm","label":"确认","command":"/delete s --confirm"}]}`} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %s: %s", part, text)
		}
	}
	if strings.Contains(text, "private continuation") {
		t.Fatal("continuation leaked")
	}
	source := errors.New("private database detail")
	failure := ClassifyError(source)
	raw, err = json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(failure, source) || strings.Contains(string(raw), source.Error()) || strings.Contains(string(raw), "Cause") {
		t.Fatalf("error wire=%s", raw)
	}
}

func TestTextResultUsesContentData(t *testing.T) {
	raw, err := json.Marshal(Result{Kind: ResultKindText, Data: TextData{Content: "accepted"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.Contains(got, `"kind":"text"`) || !strings.Contains(got, `"data":{"content":"accepted"}`) || strings.Contains(got, `"text":"`) {
		t.Fatalf("result wire=%s", got)
	}
}

func TestCommandInterruptedErrorsRemainDistinct(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
	}{{context.Canceled, CodeCanceled}, {context.DeadlineExceeded, CodeTimeout}} {
		failure := ClassifyError(tc.cause)
		if failure.Kind != ErrorInterrupted || failure.Code != tc.code || !errors.Is(failure, tc.cause) {
			t.Fatalf("failure=%+v", failure)
		}
	}
}

func TestFileRollbackErrorsKeepDomainClassification(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
		text  string
	}{{fileops.ErrFileChanged, "file_changed", "file changed; rollback refused"}, {fileops.ErrNotRegularFile, "invalid_target", "rollback target is not a regular file"}} {
		wrapped := fmt.Errorf("rollback file: %w", tc.cause)
		failure := ClassifyError(wrapped)
		if failure.Kind != ErrorRejected || failure.Code != tc.code || failure.Message != "rollback file: "+tc.text || !errors.Is(failure, tc.cause) || failure.Cause != wrapped {
			t.Fatalf("failure = %+v", failure)
		}
	}
}

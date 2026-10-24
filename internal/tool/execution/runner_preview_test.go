package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"slices"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

type batchPreviewTestDeps struct {
	runnerTestDeps
	enabled bool
	notices [][]string
}

func (d *batchPreviewTestDeps) ShouldSendPreview(context.Context, *storage.Session, llm.ToolCallRequest, string) bool {
	return d.enabled
}

func (d *batchPreviewTestDeps) SendPreview(_ context.Context, text ...string) {
	d.notices = append(d.notices, slices.Clone(text))
}

func TestBatchPreviewPreservesToolBoundaries(t *testing.T) {
	deps := &batchPreviewTestDeps{enabled: true}
	req := toolshare.RunRequest{Calls: []llm.ToolCallRequest{
		{Name: "shell", Arguments: "{}"},
		{Name: "read_file", Arguments: "{}"},
	}}
	if !sendBatchToolPreview(context.Background(), deps, req) {
		t.Fatal("batch preview was not sent")
	}
	want := []string{"正在调用 shell：{}", "正在调用 read_file：{}"}
	if len(deps.notices) != 1 || !slices.Equal(deps.notices[0], want) {
		t.Fatalf("batch notices = %q, want one call with %q", deps.notices, want)
	}
}

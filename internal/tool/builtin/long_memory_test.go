package builtin

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestLongMemoryToolsHaveAgentTag(t *testing.T) {
	for _, memoryTool := range NewLongMemoryTools(t.TempDir()) {
		if got := strings.Join(memoryTool.Info().Tags, ","); got != "agent" {
			t.Fatalf("%s tags = %q", memoryTool.Name(), got)
		}
	}
}

func TestLongMemoryToolSetIsCompact(t *testing.T) {
	tools := NewLongMemoryTools(t.TempDir())
	if len(tools) != 3 {
		t.Fatalf("tool count = %d", len(tools))
	}
	got := []string{}
	for _, memoryTool := range tools {
		got = append(got, memoryTool.Name())
	}
	if strings.Join(got, ",") != "long_memory,long_memory_search,long_memory_write" {
		t.Fatalf("tools = %q", strings.Join(got, ","))
	}
	if deps := strings.Join(tools[0].Info().DependsOn, ","); deps != "long_memory_search,long_memory_write" {
		t.Fatalf("dependencies = %q", deps)
	}
}

func TestLongMemorySearchAndWriteTools(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	var searchTool *LongMemorySearchTool
	var writeTool *LongMemoryWriteTool
	for _, memoryTool := range []tool.Tool{LongMemoryTool{store: store}, LongMemorySearchTool{store: store}, LongMemoryWriteTool{store: store}} {
		switch typed := memoryTool.(type) {
		case LongMemorySearchTool:
			searchTool = &typed
		case LongMemoryWriteTool:
			writeTool = &typed
		}
	}
	if searchTool == nil || writeTool == nil {
		t.Fatal("missing compact long memory tools")
	}

	result, err := searchTool.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "长期记忆库为空") {
		t.Fatalf("empty categories = %q", result.Content)
	}

	_, err = writeTool.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha beta","content":"完整内容 alpha"}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = searchTool.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"keywords":"alpha","brief_only":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "测试记忆") {
		t.Fatalf("search result = %q", result.Content)
	}

	_, err = writeTool.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"operation":"update","id":1,"summary":"新摘要"}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = searchTool.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"keywords":"alpha","brief_only":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "新摘要") {
		t.Fatalf("updated search result = %q", result.Content)
	}

	_, err = writeTool.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"operation":"delete","id":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = searchTool.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "长期记忆库为空") {
		t.Fatalf("categories after delete = %q", result.Content)
	}
}

func TestLongMemoryUpdateContentEditsAndRiskDetail(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	writeTool := LongMemoryWriteTool{store: store}
	searchTool := LongMemorySearchTool{store: store}
	ctx := context.Background()

	_, err := writeTool.Call(ctx, tool.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha beta","content":"第一行\n第二行\n第三行"}`)})
	if err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"operation":"update","id":1,"summary":"新摘要","content_edits":[{"operation":"replace","line":2,"new_text":"第二行已更新\n"}]}`)
	if err := writeTool.PreflightConfirmation(ctx, tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	detail, err := writeTool.RiskDetail(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"操作：更新长期记忆", "字段变化：", "摘要：摘要 -> 新摘要", "正文编辑：content_edits", "-第二行", "+第二行已更新"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("risk detail missing %q:\n%s", want, detail)
		}
	}
	if _, err := writeTool.Call(ctx, tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	result, err := searchTool.Call(ctx, tool.CallRequest{Arguments: []byte(`{"keywords":"已更新","brief_only":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"新摘要", "2 | 第二行已更新", "正文行号从 1 开始"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("search result missing %q:\n%s", want, result.Content)
		}
	}
}

func TestLongMemoryUpdateContentReplacesWholeBody(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	writeTool := LongMemoryWriteTool{store: store}
	searchTool := LongMemorySearchTool{store: store}
	ctx := context.Background()

	_, err := writeTool.Call(ctx, tool.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha","content":"旧正文"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeTool.Call(ctx, tool.CallRequest{Arguments: []byte(`{"operation":"update","id":1,"content":"新正文\n第二行"}`)}); err != nil {
		t.Fatal(err)
	}
	result, err := searchTool.Call(ctx, tool.CallRequest{Arguments: []byte(`{"keywords":"新正文","brief_only":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 | 新正文", "2 | 第二行"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("search result missing %q:\n%s", want, result.Content)
		}
	}
}

func TestLongMemoryUpdateRejectsNoChanges(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	writeTool := LongMemoryWriteTool{store: store}
	ctx := context.Background()

	_, err := writeTool.Call(ctx, tool.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha","content":"正文"}`)})
	if err != nil {
		t.Fatal(err)
	}
	err = writeTool.PreflightConfirmation(ctx, tool.CallRequest{Arguments: []byte(`{"operation":"update","id":1,"summary":"摘要"}`)})
	if err == nil || !strings.Contains(err.Error(), "edit produced no changes") {
		t.Fatalf("expected no-op error, got %v", err)
	}
}

func TestLongMemoryUpdateRejectsContentAndContentEditsTogether(t *testing.T) {
	store := newLongMemoryStore(t.TempDir())
	t.Cleanup(func() { _ = store.close() })
	writeTool := LongMemoryWriteTool{store: store}
	ctx := context.Background()

	_, err := writeTool.Call(ctx, tool.CallRequest{Arguments: []byte(`{"operation":"save","category":"测试","title":"测试记忆","summary":"摘要","keywords":"alpha","content":"正文"}`)})
	if err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"operation":"update","id":1,"content":"整段正文","content_edits":[{"operation":"insert","line":2,"new_text":"追加"}]}`)
	err = writeTool.PreflightConfirmation(ctx, tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "content and content_edits cannot be used together") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

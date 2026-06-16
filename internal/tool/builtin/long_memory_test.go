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
	var searchTool *LongMemorySearchTool
	var writeTool *LongMemoryWriteTool
	for _, memoryTool := range NewLongMemoryTools(t.TempDir()) {
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

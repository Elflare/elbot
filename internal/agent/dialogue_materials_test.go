package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	chat "elbot/internal/llm/chatcompletions"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

func TestResponsesMediaReplayAuditContainsOnlyOrderedReferences(t *testing.T) {
	ids := make(chan string, 8)
	var center *media.Manager
	f := newNativeFixture(t, func(index int, req nativeTestRequest, w http.ResponseWriter) {
		if strings.Count(inputJSON(req), `"type":"input_image"`) != 20 || !strings.Contains(inputJSON(req), "data:image/png;base64,") {
			t.Errorf("wire images missing on attempt %d", index)
		}
		emitNativeStore(w, "response", false, nativeText("done"))
	}, func(opts *testAgentOptions) {
		root := filepath.Join(t.TempDir(), "media")
		center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
		opts.Media = center
		opts.Store = nativeAuditStore{Store: opts.Store, repository: nativeAuditRepository{DialogueRepository: opts.Store.Dialogues(), created: ids}}
	})
	item, err := center.ImportBytes(t.Context(), bytes.Repeat([]byte("image-data"), 4096), media.Input{MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	segments := []platform.MessageSegment{{Type: platform.SegmentText, Text: "look"}}
	for i := 0; i < 20; i++ {
		segments = append(segments, platform.MessageSegment{Type: platform.SegmentImage, MediaID: item.ID})
	}
	ctx := platform.WithMessageContext(t.Context(), platform.MessageContext{Segments: segments})
	if err := f.agent.HandleMessage(ctx, "look"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "continue"); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		exchange, err := f.store.Dialogues().GetExchange(t.Context(), <-ids)
		if err != nil {
			t.Fatal(err)
		}
		var audit struct {
			Input json.RawMessage `json:"input"`
			Refs  []struct {
				Kind, ID string
				Index    int
			} `json:"input_refs"`
		}
		if err := json.Unmarshal([]byte(exchange.RequestJSON), &audit); err != nil {
			t.Fatal(err)
		}
		if len(audit.Input) != 0 || len(audit.Refs) != len(f.captured()[attempt].Input) || len(exchange.RequestJSON) > 16384 || strings.Contains(exchange.RequestJSON, "base64,") {
			t.Fatalf("bloated or incomplete audit: bytes=%d refs=%d", len(exchange.RequestJSON), len(audit.Refs))
		}
		for _, ref := range audit.Refs {
			if ref.Kind != "input" {
				continue
			}
			input, err := f.store.Dialogues().GetInput(t.Context(), ref.ID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(input.ItemJSON+input.MediaJSON, "base64,") {
				t.Fatal("expanded entity persisted in native input")
			}
		}
		var consumed []string
		if err := json.Unmarshal([]byte(exchange.InputIDsJSON), &consumed); err != nil {
			t.Fatal(err)
		}
		if len(consumed) != 1 {
			t.Fatalf("replay prefix was consumed again: %v", consumed)
		}
	}
	row := fixtureSession(t, f)
	rows, err := f.store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rows[0].Segments, item.ID) || strings.Contains(rows[0].Segments+rows[0].Content, "base64,") {
		t.Fatal("message contains expanded media")
	}
}

func TestChatToolPrefixSurvivesSessionRestore(t *testing.T) {
	store := newTestStore(t)
	registry := tool.NewRegistry()
	full := strings.Repeat("full result\n", 2048)
	for _, name := range []string{"one", "two"} {
		if err := registry.Register(nativeTool{name: name, run: func(context.Context, tool.CallRequest) (*tool.Result, error) { return &tool.Result{Content: full}, nil }}); err != nil {
			t.Fatal(err)
		}
	}
	client := &fakeLLM{chunks: [][]chat.Chunk{
		{{ToolCallDeltas: []chat.ToolCallDelta{{Index: 0, ID: "one", Name: "one", Args: `{}`}, {Index: 1, ID: "two", Name: "two", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	p := &fakePlatform{}
	configure := func(opts *testAgentOptions) { opts.ToolRegistry = registry }
	a := newTestAgent(t, p, client, "m", config.ProviderConfig{}, store, configure)
	if err := a.HandleMessage(t.Context(), "@tool:one @tool:two run"); err != nil {
		t.Fatal(err)
	}
	requests := client.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	prefix := requests[1].Messages[1:]
	row := onlySession(t, store, p)
	next := &fakeLLM{replies: []string{"continued"}}
	restored := newTestAgent(t, p, next, "m", config.ProviderConfig{}, store, configure)
	if err := restored.HandleMessage(t.Context(), "/resume "+row.ID); err != nil {
		t.Fatal(err)
	}
	if err := restored.HandleMessage(t.Context(), "continue"); err != nil {
		t.Fatal(err)
	}
	actual := next.chatRequests()[0].Messages
	if len(actual) < len(prefix)+1 || !reflect.DeepEqual(prefix, actual[1:len(prefix)+1]) {
		t.Fatal("restored tool prefix differs from the successful in-memory request")
	}
	results := 0
	for _, message := range prefix {
		if message.Role == llm.RoleTool {
			results++
			if message.Segments[0].Text != full {
				t.Fatal("tool result truncated")
			}
		}
	}
	if results != 2 {
		t.Fatalf("tool results=%d", results)
	}
}

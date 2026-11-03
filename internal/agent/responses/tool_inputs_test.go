package responses

import (
	"context"
	"reflect"
	"testing"

	"elbot/internal/events"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

type toolInputCommitter struct {
	commits []storage.DialogueCommit
}

func (c *toolInputCommitter) Commit(_ context.Context, commit storage.DialogueCommit, _ string) (events.LogResult, error) {
	c.commits = append(c.commits, commit)
	return events.LogResult{}, nil
}

func TestQueueToolDefinitionsKeepsHistoricalDefinitions(t *testing.T) {
	for _, source := range []string{"history", "pending"} {
		for _, change := range []string{"description", "parameters"} {
			t.Run(source+"/"+change, func(t *testing.T) {
				old := llm.ToolSchema{Name: "discover_tool", Description: "old description", Parameters: map[string]any{"type": "object"}}
				current := old
				if change == "description" {
					current.Description = "new description"
				} else {
					current.Parameters = map[string]any{"type": "object", "required": []string{"names"}}
				}
				item, err := api.AdditionalTools(api.FunctionTools([]llm.ToolSchema{old}))
				if err != nil {
					t.Fatal(err)
				}
				original := string(item.Raw)
				committer := &toolInputCommitter{}
				s := &turnState{route: &Loop{LoopDeps: LoopDeps{Messages: committer}}, tools: []llm.ToolSchema{current}}
				var inputs []storage.NativeInput
				if source == "history" {
					s.definitions, err = definitionsIn([]api.Item{item})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					inputs = []storage.NativeInput{{ID: "pending", ItemJSON: original, MediaJSON: "[]"}}
				}
				got, err := s.queueToolDefinitions(t.Context(), inputs)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, inputs) || len(committer.commits) != 0 || len(s.definitions) != 1 {
					t.Fatalf("inputs=%+v commits=%d definitions=%v", got, len(committer.commits), s.definitions)
				}
				if string(item.Raw) != original {
					t.Fatal("historical definition changed")
				}
			})
		}
	}
}

func TestQueueToolDefinitionsAddsNewNamesOnce(t *testing.T) {
	committer := &toolInputCommitter{}
	s := &turnState{
		route:   &Loop{LoopDeps: LoopDeps{Messages: committer}},
		session: &storage.Session{ID: "session"},
		tools:   []llm.ToolSchema{{Name: "first"}, {Name: "first", Description: "duplicate"}, {Name: "second"}},
	}
	inputs, err := s.queueToolDefinitions(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || len(committer.commits) != 1 {
		t.Fatalf("inputs=%+v commits=%d", inputs, len(committer.commits))
	}
	commit := committer.commits[0]
	if commit.SessionID != s.session.ID || commit.Native == nil || !reflect.DeepEqual(commit.Native.Inputs, inputs) {
		t.Fatalf("commit=%+v inputs=%+v", commit, inputs)
	}
	item, _, err := decodeQueuedInput(inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	tools, err := api.AdditionalToolDefinitions(item)
	if err != nil || len(tools) != 2 || tools[0].Name != "first" || tools[0].Description != "" || tools[1].Name != "second" {
		t.Fatalf("tools=%+v err=%v", tools, err)
	}
	// A later call also sees the definitions still pending from the first call.
	s.definitions = nil
	got, err := s.queueToolDefinitions(t.Context(), inputs)
	if err != nil || !reflect.DeepEqual(got, inputs) || len(committer.commits) != 1 {
		t.Fatalf("inputs=%+v commits=%d err=%v", got, len(committer.commits), err)
	}
	original := inputs[0]
	s.tools = append(s.tools, llm.ToolSchema{Name: "third"})
	got, err = s.queueToolDefinitions(t.Context(), inputs)
	if err != nil || len(got) != 2 || len(committer.commits) != 2 || got[0] != original {
		t.Fatalf("inputs=%+v commits=%d err=%v", got, len(committer.commits), err)
	}
	item, _, err = decodeQueuedInput(got[1])
	if err != nil {
		t.Fatal(err)
	}
	tools, err = api.AdditionalToolDefinitions(item)
	if err != nil || len(tools) != 1 || tools[0].Name != "third" {
		t.Fatalf("tools=%+v err=%v", tools, err)
	}
}

func TestToolDefinitionsRejectMalformedInputs(t *testing.T) {
	for _, raw := range []string{
		`{"type":"additional_tools","role":"user","tools":[{"type":"function","name":"read"}]}`,
		`{"type":"additional_tools","role":"developer","tools":[]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"read"},{"type":"function","name":"read"}]}`,
	} {
		item, err := api.ParseItem([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := definitionsIn([]api.Item{item}); err == nil {
			t.Fatalf("accepted malformed historical definitions: %s", raw)
		}
		s := &turnState{}
		if _, err := s.queueToolDefinitions(t.Context(), []storage.NativeInput{{ItemJSON: raw, MediaJSON: "[]"}}); err == nil {
			t.Fatalf("accepted malformed pending definitions: %s", raw)
		}
	}
}

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/runtimeinfo"
)

func nativeDefinitions(t *testing.T, request nativeTestRequest) []api.FunctionTool {
	t.Helper()
	var definitions []api.FunctionTool
	for _, raw := range request.Input {
		item, err := api.ParseItem(raw)
		if err != nil {
			t.Fatal(err)
		}
		if item.Type == "additional_tools" {
			tools, err := api.AdditionalToolDefinitions(item)
			if err != nil {
				t.Fatal(err)
			}
			definitions = append(definitions, tools...)
		}
	}
	return definitions
}

func expectNativeDefinitions(t *testing.T, request nativeTestRequest, want ...string) {
	t.Helper()
	var names []string
	for _, definition := range nativeDefinitions(t, request) {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	sort.Strings(want)
	if !slices.Equal(names, want) {
		t.Errorf("tool definitions=%v want=%v; input=%s", names, want, inputJSON(request))
	}
}

func nativeToolOutput(t *testing.T, request nativeTestRequest, callID string) string {
	t.Helper()
	for _, raw := range request.Input {
		var output struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		}
		if err := json.Unmarshal(raw, &output); err != nil {
			t.Fatal(err)
		}
		if output.Type == "function_call_output" && output.CallID == callID {
			return output.Output
		}
	}
	t.Errorf("missing function output %s", callID)
	return ""
}

func TestResponsesDiscoverySeparatesDefinitionsFromFreshCronText(t *testing.T) {
	for _, mode := range []string{"stored", "stateless"} {
		t.Run(mode, func(t *testing.T) {
			stateless := mode == "stateless"
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			var times atomic.Int64
			base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
			for _, cronTool := range builtin.NewCronTools(nil, runtimeinfo.Info{Now: func() time.Time { return base.Add(time.Duration(times.Add(1)) * time.Minute) }}) {
				if err := registry.Register(cronTool); err != nil {
					t.Fatal(err)
				}
			}
			var firstInput []json.RawMessage
			f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
				switch index {
				case 0:
					firstInput = append([]json.RawMessage(nil), request.Input...)
					expectNativeDefinitions(t, request, "discover_tool")
					emitNativeStore(w, "list", !stateless, nativeCall("list-tools", "discover_tool", `{}`))
				case 1:
					if stateless {
						expectNativeDefinitions(t, request, "discover_tool")
					} else {
						expectNativeDefinitions(t, request)
					}
					if !strings.Contains(nativeToolOutput(t, request, "list-tools"), "cron") {
						t.Error("description-only discovery result lost")
					}
					emitNativeStore(w, "discover-one", !stateless, nativeCall("find-one", "discover_tool", `{"name":"cron"}`))
				case 2:
					want := []string{"cron", "cron_query", "cron_write"}
					if stateless {
						want = append(want, "discover_tool")
					}
					expectNativeDefinitions(t, request, want...)
					output := nativeToolOutput(t, request, "find-one")
					if !strings.Contains(output, "已发现工具") || !strings.Contains(output, "2026-10-07 12:01:00") {
						t.Errorf("first discovery text=%q", output)
					}
					for _, definition := range nativeDefinitions(t, request) {
						if strings.Contains(definition.Description, "12:01:00") {
							t.Error("dynamic discovery text entered the schema")
						}
					}
					emitNativeStore(w, "discover-two", !stateless, nativeCall("find-two", "discover_tool", `{"name":"cron"}`))
				case 3:
					if stateless {
						expectNativeDefinitions(t, request, "discover_tool", "cron", "cron_query", "cron_write")
					} else {
						expectNativeDefinitions(t, request)
					}
					if output := nativeToolOutput(t, request, "find-two"); !strings.Contains(output, "2026-10-07 12:02:00") {
						t.Errorf("repeated discovery text was deduplicated: %q", output)
					}
					emitNativeStore(w, "final", !stateless, nativeText("ready"))
				case 4:
					if !stateless {
						if request.PreviousResponseID != "final" {
							t.Error("missing server continuation")
						}
						expectNativeDefinitions(t, request)
						nativeChainError(w)
						return
					}
					fallthrough
				case 5:
					expectNativeDefinitions(t, request, "discover_tool", "cron", "cron_query", "cron_write")
					for i, raw := range firstInput {
						if i >= len(request.Input) || !bytes.Equal(raw, request.Input[i]) {
							t.Error("tool discovery changed the original input prefix")
						}
					}
					body := inputJSON(request)
					for _, text := range []string{"2026-10-07 12:01:00", "2026-10-07 12:02:00"} {
						if strings.Count(body, text) != 1 {
							t.Errorf("replay lost or duplicated %s", text)
						}
					}
					emitNativeStore(w, "continued", !stateless, nativeText("continued"))
				default:
					t.Errorf("unexpected request %d", index)
				}
			}, func(opts *testAgentOptions) { opts.ToolRegistry = registry; opts.ToolsConfig.MaxRoundsPerTurn = 4 })
			ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "tester", Role: contextinfo.RoleSuperadmin})
			for _, text := range []string{"find cron", "next"} {
				if err := f.agent.HandleMessage(ctx, text); err != nil {
					t.Fatal(err)
				}
			}
			if times.Load() != 2 {
				t.Fatalf("discovery executed %d times", times.Load())
			}
			row, err := f.agent.execution.sessions.Current(ctx, f.agent.Scope(ctx))
			if err != nil {
				t.Fatal(err)
			}
			messages, err := f.store.Messages().ListBySession(ctx, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			texts := 0
			for _, message := range messages {
				if strings.Contains(message.Content, "additional_tools") {
					t.Error("definition item leaked into display history")
				}
				if message.Role == storage.RoleTool && strings.Contains(message.Content, "当前本地时间") {
					texts++
				}
			}
			if texts != 2 {
				t.Fatalf("display history has %d time hints", texts)
			}
		})
	}
}

func TestResponsesWrapperActivationAppendsSchemaAndKeepsInstructions(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "discover_tool", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		return &tool.Result{Content: "skill instructions", Metadata: map[string]any{tool.MetadataActivateTools: []string{"helper"}}}, nil
	}})
	_ = registry.Register(nativeTool{name: "helper"})
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			expectNativeDefinitions(t, request, "discover_tool")
			emitNative(w, "activated", "completed", nativeCall("activate-call", "discover_tool", `{}`))
			return
		}
		expectNativeDefinitions(t, request, "helper")
		if nativeToolOutput(t, request, "activate-call") != "skill instructions" {
			t.Error("wrapper activation removed skill instructions")
		}
		emitNative(w, "done", "completed", nativeText("done"))
	}, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
	if err := f.agent.HandleMessage(t.Context(), "@tool:discover_tool use skill"); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesFirstRequestFailureReusesPendingDefinitions(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "one"})
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		expectNativeDefinitions(t, request, "one")
		if index == 0 {
			emitNative(w, "failed", "incomplete", nativeText("partial"))
			return
		}
		if body := inputJSON(request); !strings.Contains(body, "first") || !strings.Contains(body, "retry") {
			t.Error("retry lost pending user input")
		}
		emitNative(w, "completed", "completed", nativeText("answer"))
	}, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
	if err := f.agent.HandleMessage(t.Context(), "@tool:one first"); err == nil {
		t.Fatal("expected incomplete first response")
	}
	row := fixtureSession(t, f)
	pending, err := f.store.Dialogues().PendingInputs(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending inputs=%d, want user and definitions", len(pending))
	}
	if err := f.agent.HandleMessage(t.Context(), "retry"); err != nil {
		t.Fatal(err)
	}
	for _, queued := range pending {
		input, err := f.store.Dialogues().GetInput(t.Context(), queued.ID)
		if err != nil || input.ConsumedBy == "" {
			t.Fatalf("first request input not reused: %+v %v", input, err)
		}
	}
}

func TestResponsesPendingDefinitionsSurviveRestartAndStatelessReplay(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "one"})
	_ = registry.Register(nativeTool{name: "two"})
	var opts testAgentOptions
	path := filepath.Join(t.TempDir(), "sessions.db")
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			expectNativeDefinitions(t, request, "one")
		} else {
			expectNativeDefinitions(t, request, "one", "two")
			if request.PreviousResponseID != "" {
				t.Error("stateless history was not replayed")
			}
		}
		if index == 1 {
			emitNative(w, "failed", "incomplete", nativeText("partial"))
			return
		}
		emitNativeStore(w, fmt.Sprintf("r%d", index), false, nativeText("answer"))
	}, func(options *testAgentOptions) {
		store, err := sqlite.New(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		options.Store, options.ToolRegistry = store, registry
		opts = *options
	})
	f.store = opts.Store
	if err := f.agent.HandleMessage(t.Context(), "@tool:one first"); err != nil {
		t.Fatal(err)
	}
	row := fixtureSession(t, f)
	if err := f.agent.HandleMessage(t.Context(), "@tool:two second"); err == nil {
		t.Fatal("expected incomplete response")
	}
	pending, err := f.store.Dialogues().PendingInputs(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pendingID string
	for _, input := range pending {
		if strings.Contains(input.ItemJSON, `"type":"additional_tools"`) {
			pendingID = input.ID
		}
	}
	if pendingID == "" {
		t.Fatal("failed request lost queued definitions")
	}
	if err := f.agent.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.execution.sessions.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := opts.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	opts.Store, f.store = store, store
	f.agent = mustNewWithOptions(t, opts)
	if _, err := f.agent.execution.sessions.Resume(t.Context(), f.agent.Scope(t.Context()), row.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "retry"); err != nil {
		t.Fatal(err)
	}
	input, err := f.store.Dialogues().GetInput(t.Context(), pendingID)
	if err != nil || input.ConsumedBy == "" {
		t.Fatalf("original queued definition was not consumed: %+v %v", input, err)
	}
}

func TestResponsesCompactionReloadsToolDefinitions(t *testing.T) {
	for _, extraDefinitions := range []bool{false, true} {
		t.Run(fmt.Sprintf("extraDefinitions=%v", extraDefinitions), func(t *testing.T) {
			registry := tool.NewRegistry()
			_ = registry.Register(nativeTool{name: "one"})
			f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
				expectNativeDefinitions(t, request, "one")
				if index == 1 {
					expectCompactionRequest(t, request)
					items := []string{`{"type":"compaction","encrypted_content":"compact-state"}`}
					if extraDefinitions {
						for _, item := range request.Input {
							if strings.Contains(string(item), `"type":"additional_tools"`) {
								items = append(items, string(item))
							}
						}
					}
					emitCompact(w, items...)
					return
				}
				if index == 2 && (request.PreviousResponseID != "" || !strings.Contains(inputJSON(request), "compact-state")) {
					t.Error("compacted root missing")
				}
				emitNative(w, fmt.Sprintf("r%d", index), "completed", nativeText("answer"))
			}, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
			if err := f.agent.HandleMessage(t.Context(), "@tool:one first"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.agent.CompactCurrent(t.Context(), "manual"); err != nil {
				t.Fatal(err)
			}
			if err := f.agent.HandleMessage(t.Context(), "@tool:one continue"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResponsesForkDoesNotAuthorizeHistoricalTools(t *testing.T) {
	var executed atomic.Int32
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "one", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		executed.Add(1)
		return &tool.Result{Content: "must not execute"}, nil
	}})
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "source", "completed", nativeText("source answer"))
			return
		}
		if string(request.ToolChoice) != `"none"` {
			t.Errorf("fork inherited tool permission: %s", request.ToolChoice)
		}
		emitNative(w, "branch", "completed", nativeCall("forbidden", "one", `{}`))
	}, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
	if err := f.agent.HandleMessage(t.Context(), "@tool:one source"); err != nil {
		t.Fatal(err)
	}
	row := fixtureSession(t, f)
	messages, err := f.store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.agent.execution.sessions.Fork(t.Context(), f.agent.Scope(t.Context()), messages[len(messages)-1].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "branch"); err == nil || !strings.Contains(err.Error(), "unavailable function") {
		t.Fatalf("historical tool accepted: %v", err)
	}
	if executed.Load() != 0 {
		t.Fatal("historical permission triggered a side effect")
	}
}

type nativeSchemaProvider struct{ schemas []llm.ToolSchema }

func (p *nativeSchemaProvider) Schemas(context.Context, string, *storage.Session, session.Scope) ([]llm.ToolSchema, error) {
	return p.schemas, nil
}

func TestResponsesRejectsChangedDefinitionsAndLegacySessions(t *testing.T) {
	for _, kind := range []string{"changed schema", "old format"} {
		t.Run(kind, func(t *testing.T) {
			provider := &nativeSchemaProvider{schemas: []llm.ToolSchema{{Name: "one", Description: "original", Parameters: map[string]any{"type": "object"}}}}
			f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
				emitNative(w, "first", "completed", nativeText("answer"))
			}, func(opts *testAgentOptions) { opts.ToolProvider = provider })
			if err := f.agent.HandleMessage(t.Context(), "first"); err != nil {
				t.Fatal(err)
			}
			row := fixtureSession(t, f)
			before, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "changed schema" {
				provider.schemas[0].Description = "changed"
			} else {
				_, err := f.store.Sessions().Mutate(t.Context(), row.ID, func(row *storage.Session) error {
					fields, err := storage.DecodeSessionMetadata(row.Metadata)
					if err != nil {
						return err
					}
					delete(fields, "responses_input_version")
					row.Metadata, err = fields.Encode()
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := f.agent.HandleMessage(t.Context(), "next"); err == nil || !strings.Contains(err.Error(), "新建会话") {
				t.Fatalf("unsupported definition/session continued: %v", err)
			}
			after, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID)
			if err != nil || !reflect.DeepEqual(before, after) || len(f.captured()) != 1 {
				t.Fatalf("rejection changed native history: %+v %v", after, err)
			}
		})
	}
}

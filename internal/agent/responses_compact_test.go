package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/storage"
	"elbot/internal/tool"
)

func TestResponsesRepeatedCompactionArchivesExtraOutputWithoutExecutingIt(t *testing.T) {
	ids := make(chan string, 8)
	var executed atomic.Int32
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "one", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		executed.Add(1)
		return &tool.Result{Content: "must not execute"}, nil
	}})
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		switch index {
		case 0:
			emitNative(w, "source", "completed", nativeText("source answer"))
		case 1, 2:
			expectCompactionRequest(t, request)
			if index == 2 && (len(request.Input) != 2 || !strings.Contains(inputJSON(request), "compact-1") || strings.Contains(inputJSON(request), "source input")) {
				t.Errorf("second compact did not start from the first seed: %+v", request)
			}
			emitCompact(w, nativeText("audit only"), nativeCall("unexpected", "one", "{}"),
				fmt.Sprintf(`{"type":"compaction","encrypted_content":"compact-%d","future":9007199254740993}`, index))
		case 3:
			if strings.Contains(inputJSON(request), "compact-1") || strings.Contains(inputJSON(request), "compaction_trigger") || !strings.Contains(inputJSON(request), "compact-2") {
				t.Errorf("follow-up root=%+v", request)
			}
			emitNative(w, "follow-up", "completed", nativeText("continued"))
		default:
			t.Errorf("unexpected request %d", index)
		}
	}, func(opts *testAgentOptions) {
		opts.ToolRegistry = registry
		opts.Store = nativeAuditStore{Store: opts.Store, repository: nativeAuditRepository{DialogueRepository: opts.Store.Dialogues(), created: ids}}
	})
	if err := f.agent.HandleMessage(t.Context(), "@tool:one source input"); err != nil {
		t.Fatal(err)
	}
	<-ids
	for i := 1; i <= 2; i++ {
		before := fixtureSession(t, f)
		if _, err := f.agent.CompactCurrent(t.Context(), "manual"); err != nil {
			t.Fatal(err)
		}
		after := fixtureSession(t, f)
		if after.ID == before.ID {
			t.Fatal("compaction did not create and activate a new session")
		}
		exchange, err := f.store.Dialogues().GetExchange(t.Context(), <-ids)
		if err != nil || exchange.Status != "compacted" || !strings.Contains(exchange.RequestJSON, "compaction_trigger") ||
			!strings.Contains(exchange.ResponseJSON, "audit only") || !strings.Contains(exchange.ItemsJSON, "function_call") {
			t.Fatalf("compact audit=%+v err=%v", exchange, err)
		}
		var request nativeTestRequest
		if err := json.Unmarshal([]byte(exchange.RequestJSON), &request); err != nil || inputJSON(request) != inputJSON(f.captured()[i]) {
			t.Fatalf("request audit differs from wire: %s err=%v", exchange.RequestJSON, err)
		}
		seed, err := f.store.Dialogues().Seed(t.Context(), after.ID)
		if err != nil || seed.Consumed || strings.Contains(seed.ItemsJSON, "audit only") || strings.Contains(seed.ItemsJSON, "function_call") || strings.Contains(seed.ItemsJSON, "additional_tools") || strings.Contains(seed.ItemsJSON, "compaction_trigger") {
			t.Fatalf("seed=%+v err=%v", seed, err)
		}
	}
	if executed.Load() != 0 {
		t.Fatal("compaction output executed a tool")
	}
	if err := f.agent.HandleMessage(t.Context(), "continue"); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesCompactedSeedSurvivesFailedFirstCheckpoint(t *testing.T) {
	for _, mode := range []string{"API", "commit"} {
		t.Run(mode, func(t *testing.T) {
			var fail atomic.Bool
			f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
				switch index {
				case 0:
					emitNative(w, "source", "completed", nativeText("source answer"))
				case 1:
					emitCompact(w, `{"type":"compaction","encrypted_content":"pending-seed"}`)
				case 2, 3:
					if request.PreviousResponseID != "" || strings.Count(inputJSON(request), "pending-seed") != 1 {
						t.Errorf("seed retry=%+v", request)
					}
					if mode == "API" && index == 2 {
						emitNative(w, "failed-first", "incomplete")
					} else {
						emitNative(w, "new-head", "completed", nativeText("continued"))
					}
				default:
					t.Errorf("unexpected request %d", index)
				}
			}, func(opts *testAgentOptions) {
				if mode == "commit" {
					opts.Store = nativeCommitFaultStore{Store: opts.Store, fault: func(commit storage.DialogueCommit) error {
						if commit.Native != nil && commit.Native.Checkpoint.ID != "" && fail.Swap(false) {
							return errors.New("first compacted checkpoint failed")
						}
						return nil
					}}
				}
			})
			if err := f.agent.HandleMessage(t.Context(), "source input"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.agent.CompactCurrent(t.Context(), "manual"); err != nil {
				t.Fatal(err)
			}
			row := fixtureSession(t, f)
			fail.Store(true)
			if err := f.agent.HandleMessage(t.Context(), "continue"); err == nil {
				t.Fatal("expected first follow-up failure")
			}
			seed, err := f.store.Dialogues().Seed(t.Context(), row.ID)
			if err != nil || seed.Consumed {
				t.Fatalf("failed follow-up consumed seed: %+v %v", seed, err)
			}
			if checkpoint, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID); err != nil || checkpoint != nil {
				t.Fatalf("failed follow-up advanced checkpoint: %+v %v", checkpoint, err)
			}
			if err := f.agent.HandleMessage(t.Context(), "retry"); err != nil {
				t.Fatal(err)
			}
			seed, err = f.store.Dialogues().Seed(t.Context(), row.ID)
			if err != nil || !seed.Consumed {
				t.Fatalf("successful retry did not consume seed: %+v %v", seed, err)
			}
		})
	}
}

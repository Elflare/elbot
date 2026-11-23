package responses

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
)

type copyRepository struct {
	storage.DialogueRepository
	checkpoint *storage.NativeCheckpoint
	snapshot   *storage.NativeCheckpoint
	records    *storage.NativeWindowRecords
	pending    []storage.NativeInput
}

func (r *copyRepository) CurrentCheckpoint(context.Context, string) (*storage.NativeCheckpoint, error) {
	return r.checkpoint, nil
}

func (r *copyRepository) CheckpointForMessage(context.Context, string, string) (*storage.NativeCheckpoint, error) {
	return r.snapshot, nil
}

func (r *copyRepository) LoadNativeWindow(context.Context, *storage.Session, *storage.NativeCheckpoint) (*storage.NativeWindowRecords, error) {
	return r.records, nil
}

func (r *copyRepository) PendingInputs(context.Context, string) ([]storage.NativeInput, error) {
	return append([]storage.NativeInput(nil), r.pending...), nil
}

type copyPreparer struct{}

func (copyPreparer) PrepareMaterials(_ context.Context, messages []llm.LLMMessage) (operationresult.Result[[]llm.LLMMessage], media.UsageID, error) {
	for i := range messages {
		messages[i].Segments = append([]llm.MessageSegment(nil), messages[i].Segments...)
		for j := range messages[i].Segments {
			if messages[i].Segments[j].Type == llm.SegmentImage {
				messages[i].Segments[j].URL = "https://example.invalid/image.png"
			}
		}
	}
	return operationresult.Result[[]llm.LLMMessage]{Status: operationresult.Success, Data: messages}, "", nil
}

func (copyPreparer) FinishMaterials(context.Context, media.UsageID) {}

func copyInput(t *testing.T, id, callID, text string, image bool) storage.NativeInput {
	t.Helper()
	segments := llm.TextSegments(text)
	if image {
		segments = append(segments, llm.MessageSegment{Type: llm.SegmentImage, MediaID: "media:" + strings.Repeat("a", 64)})
	}
	input, err := queuedInput("source", id, "", callID, segments)
	if err != nil {
		t.Fatal(err)
	}
	input.ID = id
	return input
}

func copyHistory(t *testing.T, stored bool) (*storage.Session, *copyRepository) {
	t.Helper()
	row := &storage.Session{ID: "source", Metadata: `{"llm_origin":{"protocol":"response","provider":"p"},"responses_input_version":2}`}
	cp := &storage.NativeCheckpoint{ID: "cp", SessionID: row.ID, ExchangeID: "exchange", ResponseID: "response", MessageID: "answer", CallsJSON: `[]`}
	input := copyInput(t, "first", "", "old question", false)
	input.ConsumedBy = cp.ExchangeID
	response, err := json.Marshal(map[string]any{"id": cp.ResponseID, "status": "completed", "store": stored})
	if err != nil {
		t.Fatal(err)
	}
	exchange := storage.NativeExchange{ID: cp.ExchangeID, SessionID: row.ID, APIType: "response", Provider: "p", BaseURL: "https://example.invalid", Status: "completed", RequestJSON: `{"store":true}`, ResponseJSON: string(response), ItemsJSON: `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]}]`, InputIDsJSON: `["first"]`}
	return row, &copyRepository{checkpoint: cp, snapshot: cp, records: &storage.NativeWindowRecords{
		Checkpoints: map[string]storage.NativeCheckpoint{cp.ID: *cp},
		Exchanges:   map[string]storage.NativeExchange{exchange.ID: exchange},
		Inputs:      map[string]storage.NativeInput{input.ID: input},
	}}
}

func TestPrepareCopyPreservesStoppedInputs(t *testing.T) {
	for _, stage := range []string{"first", "history", "seed"} {
		for _, stored := range []bool{false, true} {
			t.Run(stage+"/stored="+map[bool]string{false: "false", true: "true"}[stored], func(t *testing.T) {
				row, repo := copyHistory(t, stored)
				c := &Context{Repository: repo, Preparer: copyPreparer{}}
				if stage == "first" {
					repo.checkpoint, repo.snapshot = nil, nil
					repo.records = &storage.NativeWindowRecords{}
				}
				repo.pending = []storage.NativeInput{copyInput(t, "stopped", "", "unanswered question", true)}
				// A stopped call can also leave its native tool definitions queued.
				definitions, err := api.AdditionalTools(api.FunctionTools([]llm.ToolSchema{{Name: "read", Parameters: map[string]any{"type": "object"}}}))
				if err != nil {
					t.Fatal(err)
				}
				repo.pending = append(repo.pending, storage.NativeInput{ID: "definitions", SessionID: row.ID, ItemJSON: string(definitions.Raw), MediaJSON: `[]`})
				prepared, err := c.PrepareCopy(t.Context(), row)
				if err != nil || prepared.Status != operationresult.Success {
					t.Fatalf("copy stopped inputs: %+v %v", prepared, err)
				}
				seed := prepared.Data.Seed
				if stage == "seed" {
					// Copy again before this branch has produced its first checkpoint.
					repo.checkpoint, repo.snapshot, repo.pending = nil, nil, nil
					repo.records = &storage.NativeWindowRecords{Seed: seed}
					prepared, err = c.PrepareCopy(t.Context(), row)
					if err != nil || prepared.Status != operationresult.Success {
						t.Fatalf("copy seed: %+v %v", prepared, err)
					}
					seed = prepared.Data.Seed
				}
				if len(seed.MediaIDs) != 1 || !strings.Contains(seed.MaterialsJSON, "unanswered question") {
					t.Fatalf("lost stopped material: %+v", seed)
				}
				loaded, err := c.Load(t.Context(), row, nil, &storage.NativeWindowRecords{Seed: seed})
				if err != nil || loaded.Status != operationresult.Success {
					t.Fatalf("load copy: %+v %v", loaded, err)
				}
				next := copyInput(t, "next", "", "continue please", false)
				state := &turnState{LoopState: agentshare.LoopState{LoopInput: agentshare.LoopInput{Session: row}}, seed: seed, window: loaded.Data, route: &Loop{Context: c}}
				replayed, _, err := state.replayRequest(t.Context(), api.Request{PreviousResponseID: seed.ResponseID}, []storage.NativeInput{next}, []string{next.ID}, loaded.Data.Origin)
				if err != nil || replayed.Status != operationresult.Success || replayed.Data.PreviousResponseID != "" {
					t.Fatalf("replay copy: %+v %v", replayed, err)
				}
				assertCopyQuestions(t, replayed.Data.Input)
				if stage == "first" || !stored {
					if seed.ResponseID != "" {
						t.Fatalf("unexpected response chain: %s", seed.ResponseID)
					}
					return
				}
				if seed.ResponseID != "response" {
					t.Fatalf("lost response chain: %+v", seed)
				}
				continuation, err := c.resolveContinuation(seed, loaded.Data.Items)
				if err != nil {
					t.Fatal(err)
				}
				chained, _, err := state.resolveInputs(t.Context(), []storage.NativeInput{next}, continuation)
				if err != nil || chained.Status != operationresult.Success {
					t.Fatalf("chain copy: %+v %v", chained, err)
				}
				assertCopyQuestions(t, chained.Data)
				if strings.Contains(string(mustCopyJSON(t, chained.Data)), "old question") {
					t.Fatal("chain resent committed prefix")
				}
			})
		}
	}
}

func mustCopyJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertCopyQuestions(t *testing.T, items []api.Item) {
	t.Helper()
	raw := string(mustCopyJSON(t, items))
	if strings.Count(raw, "unanswered question") != 1 || strings.Count(raw, "continue please") != 1 || strings.Index(raw, "unanswered question") > strings.Index(raw, "continue please") || strings.Count(raw, "input_image") != 1 {
		t.Fatalf("lost, repeated, or reordered inputs: %s", raw)
	}
}

func TestPrepareCopyKeepsSealedToolResultsOnce(t *testing.T) {
	for _, status := range []string{"completed", "pending"} {
		t.Run(status, func(t *testing.T) {
			row, repo := copyHistory(t, true)
			call := storage.NativeCall{ExchangeID: "exchange", CallID: "call", Name: "read", Arguments: `{}`, Status: status, ResultInputID: "result"}
			snapshot := *repo.checkpoint
			snapshot.ID, snapshot.CallsJSON = "snapshot", string(mustCopyJSON(t, []storage.NativeCall{call}))
			repo.snapshot = &snapshot
			exchange := repo.records.Exchanges["exchange"]
			exchange.ItemsJSON = `[{"type":"function_call","call_id":"call","name":"read","arguments":"{}"}]`
			repo.records.Exchanges[exchange.ID] = exchange
			result := copyInput(t, "result", "call", "tool result", false)
			result.ExchangeID = exchange.ID
			repo.records.Inputs[result.ID] = result
			repo.pending = []storage.NativeInput{result, copyInput(t, "stopped", "", "unanswered question", false)}
			before := append([]storage.NativeInput(nil), repo.pending...)
			prepared, err := (&Context{Repository: repo}).PrepareCopy(t.Context(), row)
			if status == "pending" {
				if err != nil || prepared.Status != operationresult.Conflict {
					t.Fatalf("accepted unsealed batch: %+v %v", prepared, err)
				}
				return
			}
			if err != nil || prepared.Status != operationresult.Success {
				t.Fatalf("copy sealed batch: %+v %v", prepared, err)
			}
			for _, raw := range []string{prepared.Data.Seed.ItemsJSON, prepared.Data.Seed.ContinuationJSON} {
				if strings.Count(raw, "tool result") != 1 || strings.Count(raw, "unanswered question") != 1 || strings.Index(raw, "tool result") > strings.Index(raw, "unanswered question") {
					t.Fatalf("bad tool continuation: %s", raw)
				}
			}
			if prepared.Data.ExpectedCheckpointID != "cp" || !reflect.DeepEqual(before, repo.pending) {
				t.Fatal("copy changed source inputs or expected cursor")
			}
		})
	}
}

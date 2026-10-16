package dialogue

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/turn"
)

type materialBackend struct {
	media.LocalBackend
	reads int
}

func (b *materialBackend) Open(ctx context.Context, item *storage.Media) (io.ReadCloser, error) {
	b.reads++
	return b.LocalBackend.Open(ctx, item)
}

func TestMaterialsKeepCanonicalOriginalAndReadOnce(t *testing.T) {
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	backend := &materialBackend{LocalBackend: media.LocalBackend{Root: root}}
	center := media.NewManager(store, root, backend)
	preparer := &Preparer{Media: center}
	input := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, URL: "data:image/png;base64,aW1hZ2U=", Name: "image.png"}}}}
	canonical, err := preparer.ArchiveMaterials(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	segment := canonical[0].Segments[0]
	if segment.MediaID == "" || segment.URL != "" || input[0].Segments[0].MediaID != "" {
		t.Fatal("archive mutated input or retained image entity")
	}
	canonical = append(canonical, llm.CloneMessages(canonical)...)
	original := llm.CloneMessages(canonical)
	prepared, release, err := preparer.PrepareMaterials(t.Context(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if backend.reads != 1 || prepared[0].Segments[0].URL != input[0].Segments[0].URL || !reflect.DeepEqual(canonical, original) {
		t.Fatalf("reads=%d canonical changed=%v", backend.reads, !reflect.DeepEqual(canonical, original))
	}
	metadata, err := center.Metadata(t.Context(), segment.MediaID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(metadata.LocalPath); err != nil {
		t.Fatal(err)
	}
	// Existing references archive without opening entities, even after deletion.
	archived, err := preparer.ArchiveMaterials(t.Context(), prepared)
	if err != nil || !reflect.DeepEqual(archived, canonical) || backend.reads != 1 {
		t.Fatalf("archive re-read existing media: %v", err)
	}
	logs := 0
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "dialogue_media_unavailable" {
			logs++
			if strings.Count(record.Content, segment.MediaID) != 1 || strings.Contains(record.Content, "base64") {
				t.Errorf("unsafe or duplicated notice: %s", record.Content)
			}
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	prepared, release, err = preparer.PrepareMaterials(t.Context(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if backend.reads != 2 || logs != 1 || prepared[0].Segments[0].Type != llm.SegmentText || !reflect.DeepEqual(canonical, original) {
		t.Fatalf("missing handling: reads=%d logs=%d", backend.reads, logs)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := preparer.PrepareMaterials(ctx, canonical); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := preparer.PrepareMaterials(t.Context(), canonical); err == nil {
		t.Fatal("database error converted to a missing-media placeholder")
	}
}

func TestToolTranscriptMirrorMatchesFullDatabaseFacts(t *testing.T) {
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &storage.Session{OwnerID: "test", Platform: "cli", Mode: storage.SessionModeWork}
	if err := store.Sessions().Create(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	committer := &ToolTranscriptCommitter{Messages: &MessageStore{Dialogues: store.Dialogues(), Gate: &CommitGate{Sessions: session.NewService(store), Turns: turn.NewManager(), View: ExecutionView{Sessions: store.Sessions()}}}}
	calls := []llm.ToolCallRequest{{ID: "one", Name: "discover_tool", Arguments: `{"names":["one","two"]}`}, {ID: "two", Name: "large", Arguments: `{"all":true}`}}
	head := ToolCallStorageMessage(row.ID, "calling", "calling", calls)
	if _, err := committer.Begin(t.Context(), &head); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("完整工具说明与结果\n", 4096)
	for i, call := range calls {
		message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(full)}
		stored := ToolResultStorageMessage(row.ID, message)
		if _, err := committer.Result(t.Context(), i, call, message, &storage.ToolResultCommit{Result: &stored}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := committer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var restored []llm.LLMMessage
	for _, row := range rows {
		message, err := MessageFromStorage(row)
		if err != nil {
			t.Fatal(err)
		}
		restored = append(restored, message)
	}
	if !reflect.DeepEqual(restored, committer.Committed) || len(restored) != 3 || restored[2].Segments[0].Text != full {
		t.Fatal("committed mirror differs from complete restored transcript")
	}
	before := len(committer.Committed)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	stored := ToolResultStorageMessage(row.ID, llm.LLMMessage{Role: llm.RoleTool, Segments: llm.TextSegments("unsaved")})
	if _, err := committer.Result(t.Context(), 0, calls[0], llm.LLMMessage{}, &storage.ToolResultCommit{Result: &stored}); err == nil || len(committer.Committed) != before {
		t.Fatal("failed result entered active context")
	}
}

func TestTextSegmentsRestoreWithoutWhitespaceOrBoundaryChanges(t *testing.T) {
	for _, segments := range [][]llm.MessageSegment{
		llm.TextSegments("  leading\ntrailing \n"),
		{{Type: llm.SegmentText, Text: "left "}, {Type: llm.SegmentText, Text: "\n right"}},
		{{Type: llm.SegmentText}},
	} {
		message := llm.LLMMessage{Role: llm.RoleTool, Name: "tool", ToolCallID: "id", Segments: segments}
		stored := ToolResultStorageMessage("session", message)
		if got, err := MessageFromStorage(stored); err != nil || !reflect.DeepEqual(got, message) {
			t.Fatalf("restored=%#v want=%#v", got, message)
		}
	}
}

type countedDialogueRepository struct {
	storage.DialogueRepository
	commits         int
	mediaReferences [][]storage.MediaReference
}

func (r *countedDialogueRepository) Commit(ctx context.Context, commit storage.DialogueCommit) (events.LogResult, error) {
	r.commits++
	r.mediaReferences = append(r.mediaReferences, append([]storage.MediaReference(nil), commit.MediaReferences...))
	return r.DialogueRepository.Commit(ctx, commit)
}

func TestToolBatchSlotsAreVisibleAndUpdatedInOriginalOrder(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := &storage.Session{OwnerID: "test", Platform: "cli", Mode: storage.SessionModeWork}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	repository := &countedDialogueRepository{DialogueRepository: store.Dialogues()}
	committer := &ToolTranscriptCommitter{Messages: &MessageStore{Dialogues: repository, Gate: &CommitGate{Sessions: session.NewService(store), Turns: turn.NewManager()}}}
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("tool input"), media.Input{Name: "input.bin"})
	if err != nil {
		t.Fatal(err)
	}
	calls := []llm.ToolCallRequest{{ID: "A", Name: "a", Arguments: "{}"}, {ID: "B", Name: "b", Arguments: "{}"}, {ID: "C", Name: "c", Arguments: "{}"}}
	calls[0].Arguments = `{"media":"` + item.ID + `"}`
	calls[1].Arguments = calls[0].Arguments
	calls[2].Arguments = calls[0].Arguments
	head := ToolCallStorageMessage(row.ID, "", "", calls)
	if _, err := committer.Begin(ctx, &head); err != nil {
		t.Fatal(err)
	}
	initial, err := store.Messages().ListBySession(ctx, row.ID)
	if err != nil || len(initial) != 4 {
		t.Fatalf("initial=%+v error=%v", initial, err)
	}
	refs, err := store.MediaReferences().ListByOwner(ctx, "session_tool", row.ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != item.ID {
		t.Fatalf("initial media references=%+v error=%v", refs, err)
	}
	item, err = center.ImportBytes(ctx, []byte("prepared tool input"), media.Input{Name: "prepared.bin"})
	if err != nil {
		t.Fatal(err)
	}
	calls[1].Arguments = `{"media":"` + item.ID + `","previous":` + calls[0].Arguments + `}`
	if _, err := committer.Prepared(ctx, 1, calls[1]); err != nil {
		t.Fatal(err)
	}
	completed := map[int]bool{}
	for _, index := range []int{1, 0, 2} {
		call := calls[index]
		message := llm.LLMMessage{Role: llm.RoleTool, ToolCallID: call.ID, Name: call.Name, Segments: llm.TextSegments("final " + call.ID)}
		stored := ToolResultStorageMessage(row.ID, message)
		record := &storage.ToolCallRecord{ToolCallID: call.ID, ToolName: call.Name, Success: true, StartedAt: storage.Now(), FinishedAt: storage.Now()}
		if _, err := committer.Result(ctx, index, call, message, &storage.ToolResultCommit{Result: &stored, Record: record}); err != nil {
			t.Fatal(err)
		}
		completed[index] = true
		rows, err := store.Messages().ListBySession(ctx, row.ID)
		if err != nil || len(rows) != 4 {
			t.Fatalf("rows=%+v error=%v", rows, err)
		}
		for i, call := range calls {
			got := rows[i+1]
			if got.ID != initial[i+1].ID || got.ToolCallID != call.ID || !got.CreatedAt.Equal(initial[i+1].CreatedAt) {
				t.Fatalf("slot moved: %+v", rows)
			}
			if completed[i] && got.Content != "final "+call.ID || !completed[i] && !strings.Contains(got.Content, "outcome is unknown") {
				t.Fatalf("partial result visibility: %+v", rows)
			}
		}
		usage, err := store.ToolCalls().UsageBySession(ctx, row.ID)
		if err != nil || len(usage) != len(completed) {
			t.Fatalf("statistics not atomic: %+v %v", usage, err)
		}
	}
	if _, err := committer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if repository.commits != 4 {
		t.Fatalf("commits=%d, want init + 3 results", repository.commits)
	}
	for i, want := range []int{1, 1, 0, 0} {
		if len(repository.mediaReferences[i]) != want {
			t.Fatalf("commit %d references=%+v, want %d", i, repository.mediaReferences[i], want)
		}
	}
	refs, err = store.MediaReferences().ListByOwner(ctx, "session_tool", row.ID)
	if err != nil || len(refs) != 2 {
		t.Fatalf("prepared media reference not committed with result: %+v %v", refs, err)
	}
}

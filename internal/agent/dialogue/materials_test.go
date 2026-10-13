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
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
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
	committer := &ToolTranscriptCommitter{Messages: &MessageStore{Dialogues: store.Dialogues()}}
	calls := []llm.ToolCallRequest{{ID: "one", Name: "discover_tool", Arguments: `{"names":["one","two"]}`}, {ID: "two", Name: "large", Arguments: `{"all":true}`}}
	head := ToolCallStorageMessage(row.ID, "calling", "calling", calls)
	if _, err := committer.Begin(t.Context(), &head); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("完整工具说明与结果\n", 4096)
	for i, call := range calls {
		message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(full)}
		stored := ToolResultStorageMessage(row.ID, message)
		if _, err := committer.Result(t.Context(), i, call, message, &stored); err != nil {
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
		restored = append(restored, MessageFromStorage(row))
	}
	if !reflect.DeepEqual(restored, committer.Committed) || len(restored) != 3 || restored[2].Segments[0].Text != full {
		t.Fatal("committed mirror differs from complete restored transcript")
	}
	before := len(committer.Committed)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	stored := ToolResultStorageMessage(row.ID, llm.LLMMessage{Role: llm.RoleTool, Segments: llm.TextSegments("unsaved")})
	if _, err := committer.Result(t.Context(), 0, calls[0], llm.LLMMessage{}, &stored); err != nil || len(committer.Committed) != before {
		t.Fatal("result collection failed")
	}
	if _, err := committer.Flush(t.Context()); err == nil || len(committer.Committed) != before {
		t.Fatal("failed batch entered active context")
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
		if got := MessageFromStorage(stored); !reflect.DeepEqual(got, message) {
			t.Fatalf("restored=%#v want=%#v", got, message)
		}
	}
}

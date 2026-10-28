package media

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/testutil"
)

func TestAcquireForLLMReleasesHoldsOnSuccessAndFailure(t *testing.T) {
	for _, mode := range []string{"success", "missing media", "resolve failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := testutil.NewSQLiteStore(t)
			root := t.TempDir()
			manager := NewManager(store, root, &LocalBackend{Root: root})
			item, err := manager.ImportBytes(ctx, []byte("hello"), Input{Name: "input.txt", MIMEType: "text/plain"})
			if err != nil {
				t.Fatal(err)
			}
			segment := llm.MessageSegment{Type: llm.SegmentFile, MediaID: item.ID}
			messages := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{segment, segment}}}
			if mode == "missing media" {
				messages[0].Segments = append(messages[0].Segments, llm.MessageSegment{Type: llm.SegmentFile, MediaID: IDPrefix + strings.Repeat("f", 64)})
			}
			if mode == "resolve failure" {
				manager.FileDelivery.MaxDirectBase64Bytes = 1
			}
			resolved, release, err := manager.AcquireForLLM(ctx, messages)
			if (mode == "resolve failure") != (err != nil) {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
			if mode == "missing media" && (resolved[0].Segments[2].Type != llm.SegmentText || messages[0].Segments[2].MediaID == "") {
				t.Fatal("missing media must only change the request copy")
			}
			if mode != "resolve failure" {
				refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
				if err != nil || len(refs) != 1 || refs[0].OwnerType != "request" {
					t.Fatalf("duplicate holds or missing request ownership: %+v / %v", refs, err)
				}
				if resolved[0].Segments[0].URL == "" || messages[0].Segments[0].URL != "" {
					t.Fatal("request material did not stay in a request copy")
				}
			}
			cancel()
			release()
			refs, err := store.MediaReferences().ListMediaIDs(context.Background(), item.ID)
			if err != nil || len(refs) != 0 {
				t.Fatalf("holds survived release or error: %+v / %v", refs, err)
			}
		})
	}
}

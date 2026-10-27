package execution

import (
	"context"

	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/dispatch"

	"elbot/internal/media"
)

type orderedMediaSender struct {
	t        *testing.T
	contents []string
	names    []string
}

func (s *orderedMediaSender) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	for _, out := range outputs {
		if out.Kind != delivery.KindImage && out.Kind != delivery.KindFile && out.Kind != delivery.KindRecord {
			continue
		}
		if out.Source.MediaID != "" || out.Source.URL != "" || len(out.Source.Data) != 0 {
			s.t.Fatalf("unresolved source: %#v", out.Source)
		}
		data, err := os.ReadFile(out.Source.Path)
		if err != nil {
			s.t.Fatal(err)
		}
		s.contents = append(s.contents, string(data))
		s.names = append(s.names, out.Name)
	}
	return delivery.Receipt{
		PlatformMessageIDs: []string{"sent-1"},
		SentMessages: []delivery.SentMessage{{
			PlatformMessageID: "sent-1",
			Platform:          "qqonebot",
			ScopeID:           "group:9",
			OutputIndexes:     []int{0, 1, 2},
		}},
	}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (*orderedMediaSender) Name() string { return "qqonebot" }
func (s *orderedMediaSender) SendTo(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return s.SendChat(ctx, noticeOutputs)
}

func TestOutputMediaSourcesAreCanonicalAndReceiptOrderPersists(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	path := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(path, []byte("path"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("url"))
	}))
	defer server.Close()

	sender := &orderedMediaSender{t: t}
	dispatcher := dispatch.New(dispatch.Options{Primary: sender, Store: store, Media: center, MediaRetentionDays: 7})
	messageCtx := contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ScopeID: "group:9"}})
	outputs := []delivery.Output{
		{Kind: delivery.KindImage, Name: "https://example.com/private/remote.png?rkey=name-secret", Source: delivery.Source{URL: server.URL}},
		{Kind: delivery.KindFile, Name: "local.txt", Source: delivery.Source{Path: path}},
		{Kind: delivery.KindRecord, Name: "voice.ogg", Source: delivery.Source{Data: []byte("data"), MIMEType: "audio/ogg"}},
	}
	if _, _, err := dispatcher.SendChat(messageCtx, outputs); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sender.contents, ","); got != "url,path,data" {
		t.Fatalf("sent contents = %q", got)
	}
	if got := strings.Join(sender.names, ","); got != "remote.png,local.txt,voice.ogg" || strings.Contains(got, "name-secret") {
		t.Fatalf("sent names = %q", got)
	}
	cached, err := store.Media().FindOutputs(ctx, "qqonebot", "group:9", "sent-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 3 || cached[0].Kind != "image" || cached[1].Kind != "file" || cached[2].Kind != "record" {
		t.Fatalf("cached outputs = %#v", cached)
	}
	for i := range cached {
		if cached[i].SegmentIndex != i || !media.ValidID(cached[i].MediaID) {
			t.Fatalf("cached output %d = %#v", i, cached[i])
		}
	}
}

func TestMediaReceiptKeepsDuplicatesAndPartialSuccess(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("same"), media.Input{Name: "same.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	outputs := []delivery.Output{
		{Kind: delivery.KindImage, Source: delivery.Source{MediaID: item.ID}},
		{Kind: delivery.KindImage, Source: delivery.Source{MediaID: item.ID}},
	}
	receipt := delivery.Receipt{
		PlatformMessageIDs: []string{"sent"},
		SentMessages:       []delivery.SentMessage{{PlatformMessageID: "sent", Platform: "qqonebot", ScopeID: "group:1", OutputIndexes: []int{0, 1}}},
	}
	sender := mediaSendFunc(func([]delivery.Output) (delivery.Receipt, events.LogResult, error) {
		{
			err := fmt.Errorf("later output failed")
			logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
			if err != nil {
				logResult.ResultStatus = events.ResultFailed
				logResult.Content = err.Error()
				if err == context.Canceled {
					logResult.ResultStatus = events.ResultCanceled
				}
			}
			return receipt, logResult, err
		}
	})
	dispatcher := dispatch.New(dispatch.Options{Primary: sender, Store: store, Media: center, MediaRetentionDays: 7})
	got, _, sendErr := dispatcher.SendTo(ctx, delivery.Target{}, outputs)
	if sendErr == nil || len(got.PlatformMessageIDs) != 1 {
		t.Fatalf("receipt/error = %#v/%v", got, sendErr)
	}
	cached, err := store.Media().FindOutputs(ctx, "qqonebot", "group:1", "sent", time.Now())
	if err != nil || len(cached) != 2 || cached[0].MediaID != item.ID || cached[1].MediaID != item.ID || cached[0].SegmentIndex != 0 || cached[1].SegmentIndex != 1 {
		t.Fatalf("cached duplicates = %#v %v", cached, err)
	}
}

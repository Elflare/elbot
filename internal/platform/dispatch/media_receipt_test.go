package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/testutil/mediatest"
)

func TestIncompleteMediaReceiptReportsStorageFailure(t *testing.T) {
	for _, missing := range []string{"platform", "scope", "message"} {
		t.Run(missing, func(t *testing.T) {
			store := testutil.NewSQLiteStore(t)
			root := t.TempDir()
			center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
			message := delivery.SentMessage{Platform: "primary", ScopeID: "group:9", PlatformMessageID: "sent", OutputIndexes: []int{0}}
			switch missing {
			case "platform":
				message.Platform = ""
			case "scope":
				message.ScopeID = ""
			case "message":
				message.PlatformMessageID = ""
			}
			want := delivery.Receipt{PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{message}}
			calls := 0
			sender := testSender{send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				calls++
				return want, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
			}}
			var warnings []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "cache_sent_media_receipt_failed" {
					warnings = append(warnings, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			router := New(Options{Primary: sender, Media: center, PublishUsageFinished: mediatest.Publisher(center), MediaRetentionDays: 7, SaveMediaOutput: store.Media().SaveOutput})
			got, result, err := router.SendChat(t.Context(), []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("image")}}})
			if err != nil || calls != 1 || result.ResultStatus != events.ResultSucceeded || !reflect.DeepEqual(got, want) {
				t.Fatalf("send changed: receipt=%#v result=%#v err=%v calls=%d", got, result, err, calls)
			}
			if len(warnings) != 1 || warnings[0].Level != slog.LevelWarn || !strings.Contains(warnings[0].Content, "output media requires") {
				t.Fatalf("storage validation warning missing: %#v", warnings)
			}
		})
	}
}

func TestRouterPreservesOutputActions(t *testing.T) {
	want := []delivery.Output{{Kind: delivery.KindText, Text: "confirm", Actions: []delivery.Action{{ID: "c", Label: "确认", Command: "/confirm"}}}}
	var got []delivery.Output
	sender := testSender{send: func(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		got = outputs
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}}
	router := New(Options{Primary: sender})
	if _, _, err := router.SendChat(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outputs=%#v want=%#v", got, want)
	}
}

type releasingMedia struct {
	*media.MediaCenter
	resolved, released int
	failAt             int
}

func (m *releasingMedia) ResolveForOutput(ctx context.Context, id string) (delivery.Source, media.UsageID, error) {
	m.resolved++
	if m.resolved == m.failAt {
		return delivery.Source{}, "", errors.New("resolve failed")
	}
	source, release, err := m.MediaCenter.ResolveForOutput(ctx, id)
	return source, release, err
}

func (m *releasingMedia) PublishUsageFinished(ctx context.Context, event media.UsageFinishedEvent) {
	m.released += len(event.UsageIDs)
	m.MediaCenter.PublishUsageFinished(ctx, event)
}

func TestMediaReceiptCallbackPreservesPartialSendAndReleasesSources(t *testing.T) {
	for _, cacheFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "cache failure"}[cacheFails], func(t *testing.T) {
			store := testutil.NewSQLiteStore(t)
			root := t.TempDir()
			center := &releasingMedia{MediaCenter: mediatest.New(t, media.MediaCenterDeps{Store: store}, root)}
			wantErr := errors.New("second message failed")
			wantReceipt := delivery.Receipt{PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{
				{Platform: "primary", ScopeID: "group:9", PlatformMessageID: "sent", OutputIndexes: []int{0}},
			}}
			calls := 0
			sender := testSender{send: func(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				calls++
				if len(outputs) != 2 || outputs[0].Source.Path == "" || center.released != 0 {
					t.Fatalf("media unavailable during send: %#v", outputs)
				}
				return wantReceipt, events.LogResult{ResultStatus: events.ResultFailed}, wantErr
			}}
			var saved []storage.MediaOutput
			router := New(Options{Primary: sender, Media: center, PublishUsageFinished: mediatest.Publisher(center), MediaRetentionDays: 7,
				SaveMediaOutput: func(_ context.Context, output storage.MediaOutput) error {
					saved = append(saved, output)
					if cacheFails {
						return errors.New("cache unavailable")
					}
					return nil
				},
			})
			outputs := []delivery.Output{
				{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("first")}},
				{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("second")}},
			}
			receipt, logResult, err := router.SendChat(t.Context(), outputs)
			if err != wantErr || !reflect.DeepEqual(receipt, wantReceipt) || logResult.ResultStatus != events.ResultFailed || calls != 1 {
				t.Fatalf("send changed: %#v %v %d", receipt, err, calls)
			}
			if len(saved) != 1 || saved[0].Platform != "primary" || saved[0].ScopeID != "group:9" || saved[0].MessageID != "sent" || saved[0].SegmentIndex != 0 || !media.ValidID(saved[0].MediaID) {
				t.Fatalf("actual receipt association lost: %#v", saved)
			}
			if center.released != 2 || outputs[0].Source.MediaID != "" {
				t.Fatalf("release=%d original=%#v", center.released, outputs)
			}
		})
	}
}

func TestMediaResolutionFailureReleasesPreparedSources(t *testing.T) {
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	center := &releasingMedia{MediaCenter: mediatest.New(t, media.MediaCenterDeps{Store: store}, root), failAt: 2}
	router := New(Options{Primary: testSender{send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		t.Fatal("sent after media resolution failure")
		return delivery.Receipt{}, events.LogResult{}, nil
	}}, Media: center, PublishUsageFinished: mediatest.Publisher(center)})
	_, _, err := router.SendChat(t.Context(), []delivery.Output{
		{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("first")}},
		{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("second")}},
	})
	if err == nil || center.released != 1 {
		t.Fatalf("err=%v released=%d", err, center.released)
	}
}

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
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
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
	dispatcher := New(Options{Primary: sender, SaveMediaOutput: store.Media().SaveOutput, Media: center, PublishUsageFinished: mediatest.Publisher(center), MediaRetentionDays: 7})
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
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
	item, err := center.ImportBytes(ctx, []byte("same"), media.Input{Name: "same.png", MIMEType: "image/png"}, media.ImportLimits{})
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
	dispatcher := New(Options{Primary: sender, SaveMediaOutput: store.Media().SaveOutput, Media: center, PublishUsageFinished: mediatest.Publisher(center), MediaRetentionDays: 7})
	got, _, sendErr := dispatcher.SendTo(ctx, delivery.Target{}, outputs)
	if sendErr == nil || len(got.PlatformMessageIDs) != 1 {
		t.Fatalf("receipt/error = %#v/%v", got, sendErr)
	}
	cached, err := store.Media().FindOutputs(ctx, "qqonebot", "group:1", "sent", time.Now())
	if err != nil || len(cached) != 2 || cached[0].MediaID != item.ID || cached[1].MediaID != item.ID || cached[0].SegmentIndex != 0 || cached[1].SegmentIndex != 1 {
		t.Fatalf("cached duplicates = %#v %v", cached, err)
	}
}

func TestMediaReceiptDoesNotGuessFallbackOrRetryCacheFailure(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	outputs := []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("image")}}}
	sender := mediaSendFunc(func([]delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return delivery.Receipt{PlatformMessageIDs: []string{"fallback"}}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
	router := New(Options{Primary: sender, SaveMediaOutput: store.Media().SaveOutput, Media: center, PublishUsageFinished: mediatest.Publisher(center), MediaRetentionDays: 7})
	_, _, err := router.SendChat(ctx, outputs)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := store.Media().FindOutputs(ctx, "telegram", "group:9", "fallback", time.Now())
	if err != nil || len(cached) != 0 {
		t.Fatalf("fallback cache = %#v / %v", cached, err)
	}
	calls := 0
	sender = mediaSendFunc(func([]delivery.Output) (delivery.Receipt, events.LogResult, error) {
		calls++
		return delivery.Receipt{
			PlatformMessageIDs: []string{"sent"},
			SentMessages:       []delivery.SentMessage{{Platform: "telegram", ScopeID: "group:9", PlatformMessageID: "sent", OutputIndexes: []int{0}}},
		}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})
	router = New(Options{Primary: sender, SaveMediaOutput: func(context.Context, storage.MediaOutput) error { return errors.New("cache unavailable") }, Media: center, PublishUsageFinished: mediatest.Publisher(center), MediaRetentionDays: 7})
	receipt, _, err := router.SendChat(ctx, outputs)
	if err != nil || calls != 1 || len(receipt.PlatformMessageIDs) != 1 {
		t.Fatalf("cache failure changed send result: %#v %d %v", receipt, calls, err)
	}
}

type mediaSendFunc func([]delivery.Output) (delivery.Receipt, events.LogResult, error)

func (mediaSendFunc) Name() string { return "cli" }

func (f mediaSendFunc) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return f(outputs)
}

func (f mediaSendFunc) SendTo(_ context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return f(noticeOutputs)
}

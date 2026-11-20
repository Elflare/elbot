package chat_history

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	toolmedia "elbot/internal/tool/media"
	"elbot/internal/tool/share"
	"path/filepath"
	"testing"
)

type countedHistory struct {
	storage.ChatHistoryRepository
	reads int
}

func (r *countedHistory) GetBySeq(ctx context.Context, platform string, kind contextinfo.ChatType, id string, seq int64, reply bool) (*storage.ChatMessage, error) {
	r.reads++
	return r.ChatHistoryRepository.GetBySeq(ctx, platform, kind, id, seq, reply)
}

type mediaProbe struct {
	*toolmedia.MediaRuntime
	ids int
}

func (p *mediaProbe) HistoryIDs(context.Context, media.HistoryMessage) (map[int]string, error) {
	p.ids++
	return map[int]string{2: "cached"}, nil
}
func (p *mediaProbe) GetHistoryMediaBatch(_ context.Context, req []media.HistoryMediaRequest, _ func(context.Context, string, media.PlatformMedia, int64) (operationresult.Result[delivery.Source], error), _ media.HistoryFetchOptions) ([]share.HistoryMediaResult, error) {
	return make([]share.HistoryMediaResult, len(req)), nil
}
func TestHistoryReadsByPurpose(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := store.Repository()
	reply := storage.ChatReply{MessageID: "reply:opaque-id", SenderID: "sender", SenderName: "名字", Text: "quoted"}
	row := storage.ChatMessage{Platform: "p", ChatID: "s", PlatformMessageID: "opaque/id", SenderID: "author", SenderName: "nickname", Text: "mixed", ReplyToPlatformMessageID: reply.MessageID, Metadata: storage.MarshalChatMetadata(reply), Segments: storage.MarshalChatSegments([]storage.ChatSegment{
		{Type: "text", Text: "before"}, {Type: "file", PlatformFileID: "file"}, {Type: "image", PlatformFileID: "first"}, {Type: "text", Text: "between"}, {Type: "image", PlatformFileID: "second"},
	})}
	if err := repo.Append(ctx, &row); err != nil {
		t.Fatal(err)
	}
	counter := &countedHistory{ChatHistoryRepository: repo}
	probe := &mediaProbe{}
	d := Deps{Repository: counter, Media: probe}
	source := contextinfo.Source{Platform: "p", ChatID: "s"}
	ctx = contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: source})
	got, err := d.replyMessage(ctx, 1)
	if err != nil || got.Status != operationresult.Success || got.Data.PlatformMessageID != "opaque/id" || probe.ids != 0 {
		t.Fatalf("reply=%+v err=%v", got, err)
	}
	display, err := d.message(ctx, *got.Data)
	if err != nil || display.Reply.Text != "quoted" || len(display.Media) != 3 || display.Media[0].IsImage || display.Media[1].MediaID != "cached" || !display.Media[2].IsImage {
		t.Fatalf("display=%+v err=%v", display, err)
	}
	before := counter.reads
	res, err := d.getMedia(ctx, MediaRequest{Seqs: []int64{1, 1}, MediaIndex: [][]int{{2, 3}, {2}}}, true)
	if err != nil || res.Status != operationresult.Success || len(res.Data) != 3 || counter.reads-before != 1 || probe.ids != 1 {
		t.Fatalf("media=%+v reads=%d cached lookups=%d err=%v", res, counter.reads-before, probe.ids, err)
	}
	source.ChatID = "other"
	if res, err := d.getMessage(ctx, source, 1, false); err != nil || res.Status != operationresult.NotFound {
		t.Fatalf("scope: %+v %v", res, err)
	}
}

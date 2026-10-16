package builtin

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/media"
	"elbot/internal/storage"
)

type textHistoryIOStore struct {
	storage.Store
	media *textHistoryIOMedia
}

func (s textHistoryIOStore) Media() storage.MediaRepository { return s.media }

type textHistoryIOMedia struct {
	storage.MediaRepository
	reads int
}

func (r *textHistoryIOMedia) FindHistory(context.Context, string, string, string) ([]storage.HistoryMedia, error) {
	r.reads++
	return nil, errors.New("media must not be queried")
}

func TestTextHistoryFormattingSkipsMediaQueries(t *testing.T) {
	repo := &textHistoryIOMedia{}
	center := &media.Manager{Store: textHistoryIOStore{media: repo}}
	row := storage.ChatMessage{PlatformMessageID: "message", Text: "plain text"}
	got, err := formatChatHistoryMedia(t.Context(), row, "", center)
	if err != nil || repo.reads != 0 || got != formatChatHistoryLine(row, "") {
		t.Fatalf("formatted=%q reads=%d error=%v", got, repo.reads, err)
	}
}

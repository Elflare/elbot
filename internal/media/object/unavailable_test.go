package object

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"elbot/internal/media/share"
	"elbot/internal/storage"
)

func TestUnavailableObjectAndStorageContracts(t *testing.T) {
	m, store, _ := newTestManager(t)
	absent := share.IDPrefix + strings.Repeat("f", 64)
	item := importTestMedia(t, m, "deleting content")
	if _, err := store.Media().ClaimOrphans(t.Context(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{absent, item.ID} {
		if _, err := m.Info(t.Context(), id); !share.IsUnavailable(err) || !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Info: %v", err)
		}
		if _, err := m.Hold(t.Context(), id); !share.IsUnavailable(err) || !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Hold: %v", err)
		}
		if err := store.Media().Touch(t.Context(), id, time.Now()); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Touch: %v", err)
		}
		if err := store.MediaReferences().Add(t.Context(), &storage.MediaReference{MediaID: id, OwnerType: "request", OwnerID: "test", Purpose: "temporary"}); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Add: %v", err)
		}
	}
	valid := importTestMedia(t, m, "valid content")
	err := store.MediaReferences().AddAll(t.Context(), []storage.MediaReference{{MediaID: valid.ID, OwnerType: "request", OwnerID: "first", Purpose: "temporary"}, {MediaID: absent, OwnerType: "request", OwnerID: "second", Purpose: "temporary"}})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(t.Context(), valid.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("failed batch was not atomic: %+v %v", refs, err)
	}
}

func TestUnavailableContentDoesNotHideOtherFaults(t *testing.T) {
	m, _, backend := newTestManager(t)
	m.remote = &testBackend{}
	item := importTestMedia(t, m, "content")
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close(t.Context())
	for _, fault := range []error{os.ErrNotExist, os.ErrPermission, context.Canceled} {
		backend.open = func(context.Context, string) (io.ReadCloser, error) { return nil, fault }
		_, err := held.Open(t.Context())
		if share.IsUnavailable(err) != errors.Is(fault, os.ErrNotExist) || !errors.Is(err, fault) {
			t.Fatalf("Open: %v", err)
		}
		_, err = held.SignedURL(t.Context())
		if share.IsUnavailable(err) != errors.Is(fault, os.ErrNotExist) || !errors.Is(err, fault) {
			t.Fatalf("SignedURL: %v", err)
		}
	}
	// 关闭阶段的文件错误不能伪装成对象内容缺失。
	backend.open = func(context.Context, string) (io.ReadCloser, error) {
		return &testReader{Reader: strings.NewReader("content"), closeErr: os.ErrNotExist}, nil
	}
	if _, err := held.SignedURL(t.Context()); share.IsUnavailable(err) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("close fault misclassified: %v", err)
	}
}
